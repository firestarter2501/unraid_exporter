#!/bin/sh
# End-to-end test, run via `./dev.sh e2e`.
# Starts a podman pod with the mock Unraid API, the exporter image and
# Prometheus, then checks the exporter output and that Prometheus scrapes it.
# The exporter is configured only through environment variables, as the
# Unraid Docker template does. Everything is removed afterwards.
set -eu

PODMAN=${PODMAN:-podman}
IMAGE=${IMAGE:-localhost/unraid_exporter:latest}
MOCK_IMAGE=${MOCK_IMAGE:-localhost/unraid_exporter-mock:latest}
PROM_IMAGE=${PROM_IMAGE:-docker.io/prom/prometheus:v3.15.0}
NAME=unraid-exporter-e2e-$$
WORK=$(mktemp -d "${TMPDIR:-/tmp}/unraid-e2e.XXXXXX")

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- exporter logs ---"
		$PODMAN logs "$NAME-exporter" 2>&1 | tail -50 || true
	fi
	$PODMAN pod rm -f "$NAME" >/dev/null 2>&1 || true
	rm -rf "$WORK"
	exit "$status"
}
trap cleanup EXIT INT TERM

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

cat >"$WORK/prometheus.yml" <<PROM
global:
  scrape_interval: 2s
scrape_configs:
  - job_name: unraid
    static_configs:
      - targets: ["localhost:9703"]
PROM

$PODMAN pod create --name "$NAME" -p 127.0.0.1::9703 -p 127.0.0.1::9090 >/dev/null
$PODMAN run -d --pod "$NAME" --name "$NAME-mock" "$MOCK_IMAGE" -api-key test-api-key >/dev/null
# Same ExtraParams and environment-only configuration as deploy/unraid/unraid-exporter.xml.
$PODMAN run -d --pod "$NAME" --name "$NAME-exporter" \
	--read-only --cap-drop all --security-opt no-new-privileges \
	-e UNRAID_URL=http://localhost:8080 \
	-e UNRAID_API_KEY=test-api-key \
	-e UNRAID_EXPORTER_COLLECTORS=info,array,metrics,temperature,network,docker,vms,shares,notifications,ups,disks \
	-e UNRAID_EXPORTER_NETWORK_EXCLUDE='^eth1$' \
	"$IMAGE" >/dev/null
$PODMAN run -d --pod "$NAME" --name "$NAME-prometheus" \
	-v "$WORK/prometheus.yml:/etc/prometheus/prometheus.yml:ro,Z" "$PROM_IMAGE" >/dev/null

EXPORTER_PORT=$($PODMAN port "$NAME-exporter" 9703 | sed 's/.*://')
PROM_PORT=$($PODMAN port "$NAME-exporter" 9090 | sed 's/.*://')
EXPORTER=http://127.0.0.1:$EXPORTER_PORT
PROM=http://127.0.0.1:$PROM_PORT

wait_for() {
	i=0
	until curl -fsS "$1" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt 60 ] || fail "timeout waiting for $1"
		sleep 0.5
	done
}

wait_for "$EXPORTER/-/healthy"
curl -fsS "$EXPORTER/metrics" >"$WORK/metrics.txt"

expect() {
	grep -qF -- "$1" "$WORK/metrics.txt" || fail "metric not found: $1"
	echo "ok: $1"
}

expect 'unraid_up 1'
for c in info array metrics temperature network docker vms shares notifications ups disks; do
	expect "unraid_exporter_collector_success{collector=\"$c\"} 1"
done
expect 'unraid_info{api_version="4.25.0",arch="x64",hostname="tower",kernel="6.12.24-Unraid",unraid_version="7.2.0"} 1'
expect 'unraid_array_started 1'
expect 'unraid_array_size_bytes 1.2288e+13'
expect 'unraid_array_disk_temperature_celsius{device="sdc",name="disk1",type="data"} 33'
expect 'unraid_docker_container_running{name="plex"} 1'
expect 'unraid_vm_running{name="Windows 11"} 1'
expect 'unraid_ups_online{model="Back-UPS ES 700",ups="APC Back-UPS"} 1'
expect 'unraid_exporter_build_info{'
expect 'unraid_network_up{interface="eth0",operstate="up"} 1'
if grep -q 'interface="eth1"' "$WORK/metrics.txt"; then
	fail "UNRAID_EXPORTER_NETWORK_EXCLUDE was not applied"
fi
echo "ok: network exclude from environment applied"

# The image HEALTHCHECK (shown in Unraid's Docker UI) must pass.
$PODMAN healthcheck run "$NAME-exporter" || fail "image healthcheck failed"
echo "ok: image healthcheck passes"

# Prometheus must parse the exposition and see the target as healthy.
wait_for "$PROM/-/ready"
i=0
while :; do
	out=$(curl -fsS "$PROM/api/v1/query?query=unraid_up" || true)
	case "$out" in
	*'"value":['*'"1"]'*) break ;;
	esac
	i=$((i + 1))
	[ "$i" -lt 60 ] || fail "prometheus did not ingest unraid_up=1: $out"
	sleep 1
done
echo "ok: prometheus scraped unraid_up=1"

targets=$(curl -fsS "$PROM/api/v1/targets")
case "$targets" in
*'"health":"up"'*) echo "ok: prometheus target healthy" ;;
*) fail "prometheus target not healthy: $targets" ;;
esac

# A wrong API key must surface as unraid_up 0, not a crash.
$PODMAN run -d --pod "$NAME" --name "$NAME-badkey" \
	-e UNRAID_URL=http://localhost:8080 -e UNRAID_API_KEY=wrong \
	-e UNRAID_EXPORTER_LISTEN_ADDRESS=:9704 "$IMAGE" >/dev/null
sleep 1
bad=$($PODMAN exec "$NAME-prometheus" wget -qO- http://localhost:9704/metrics)
echo "$bad" | grep -qx 'unraid_up 0' || fail "expected unraid_up 0 with a wrong API key"
echo "ok: wrong API key reports unraid_up 0"

# An invalid environment variable must stop the container with a clear error.
if $PODMAN run --rm -e UNRAID_URL=http://x -e UNRAID_API_KEY=k -e UNRAID_TIMEOUT=ten "$IMAGE" >"$WORK/bad.log" 2>&1; then
	fail "invalid UNRAID_TIMEOUT was accepted"
fi
grep -q 'invalid UNRAID_TIMEOUT' "$WORK/bad.log" || fail "unexpected error: $(cat "$WORK/bad.log")"
echo "ok: invalid environment variable rejected"

echo "E2E PASSED"
