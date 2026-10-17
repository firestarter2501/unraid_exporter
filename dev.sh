#!/bin/sh
# Development entrypoint. Every task runs inside containers via podman, so the
# only host requirement is podman itself. Go module / build caches live in
# named podman volumes (see `./dev.sh clean`).
set -eu

cd "$(dirname "$0")"

PODMAN=${PODMAN:-podman}
GO_IMAGE=${GO_IMAGE:-docker.io/library/golang:1.26-trixie}
LINT_IMAGE=${LINT_IMAGE:-docker.io/golangci/golangci-lint:v2.14.0}
IMAGE=${IMAGE:-localhost/unraid_exporter}
MOCK_IMAGE=${MOCK_IMAGE:-localhost/unraid_exporter-mock}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
REVISION=${REVISION:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}

go_run() {
	$PODMAN run --rm --userns=keep-id \
		-v "$PWD:/src:Z" -w /src \
		-v unraid-exporter-gomod:/go/pkg \
		-v unraid-exporter-gocache:/tmp/gocache \
		-e HOME=/tmp -e GOTELEMETRY=off -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
		"$GO_IMAGE" "$@"
}

# --format docker keeps HEALTHCHECK, which Unraid's Docker UI displays.
build_image() {
	$PODMAN build --format docker --build-arg GO_IMAGE="$GO_IMAGE" \
		--build-arg VERSION="$VERSION" --build-arg REVISION="$REVISION" \
		-t "$IMAGE:$VERSION" -t "$IMAGE:latest" -f Containerfile .
}

build_mock_image() {
	$PODMAN build --build-arg GO_IMAGE="$GO_IMAGE" --target mockunraid \
		-t "$MOCK_IMAGE:latest" -f Containerfile .
}

usage() {
	cat <<USAGE
Usage: ./dev.sh <task>

  tidy      go mod tidy
  fmt       check gofmt (fmt-fix to rewrite)
  vet       go vet
  lint      golangci-lint
  test      unit tests with the race detector
  golden    regenerate golden metric files
  image     build the exporter image ($IMAGE)
  e2e       end-to-end test: mock Unraid + exporter + Prometheus in a podman pod
  check     fmt + vet + lint + test + e2e
  run       run the exporter against a real server, configured like
            production from deploy/unraid-exporter.env
  clean     remove built images and cache volumes
USAGE
}

task=${1:-help}
case "$task" in
tidy) go_run go mod tidy ;;
fmt)
	out=$(go_run gofmt -l .)
	if [ -n "$out" ]; then
		echo "gofmt needed:"
		echo "$out"
		exit 1
	fi
	;;
fmt-fix) go_run gofmt -w . ;;
vet) go_run go vet ./... ;;
lint)
	$PODMAN run --rm --userns=keep-id -v "$PWD:/src:Z" -w /src \
		-v unraid-exporter-gomod:/go/pkg -v unraid-exporter-lintcache:/tmp/lintcache \
		-e HOME=/tmp -e GOTELEMETRY=off -e GOLANGCI_LINT_CACHE=/tmp/lintcache -e GOCACHE=/tmp/lintcache/go -e GOFLAGS=-buildvcs=false \
		"$LINT_IMAGE" golangci-lint run ./...
	;;
test) go_run go test -race -count=1 ./... ;;
golden) go_run go test ./internal/collector -run TestCollectorsGolden -count=1 -update ;;
image) build_image ;;
mock-image) build_mock_image ;;
e2e)
	build_image
	build_mock_image
	IMAGE="$IMAGE:latest" MOCK_IMAGE="$MOCK_IMAGE:latest" PODMAN="$PODMAN" ./test/e2e.sh
	;;
check)
	"$0" fmt
	"$0" vet
	"$0" lint
	"$0" test
	"$0" e2e
	;;
run)
	[ -f deploy/unraid-exporter.env ] || {
		echo "copy deploy/unraid-exporter.env.example to deploy/unraid-exporter.env and edit it" >&2
		exit 1
	}
	build_image
	$PODMAN run --rm -p 9703:9703 --read-only --cap-drop all --security-opt no-new-privileges \
		--env-file deploy/unraid-exporter.env "$IMAGE:latest"
	;;
clean)
	$PODMAN rmi -i "$IMAGE:latest" "$MOCK_IMAGE:latest" || true
	$PODMAN volume rm -f unraid-exporter-gomod unraid-exporter-gocache unraid-exporter-lintcache || true
	;;
help | -h | --help) usage ;;
*)
	usage >&2
	exit 2
	;;
esac
