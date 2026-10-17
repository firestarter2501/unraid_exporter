package collector

import (
	"flag"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/firestarter2501/unraid_exporter/internal/mockunraid"
	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

var update = flag.Bool("update", false, "update golden files")

const testKey = "test-api-key"

func newTestExporter(t *testing.T, collectors []string) *prometheus.Registry {
	t.Helper()
	srv := httptest.NewServer(mockunraid.Handler("testdata", testKey))
	t.Cleanup(srv.Close)
	client, err := unraid.NewClient(unraid.Options{URL: srv.URL, APIKey: testKey, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := New(client, collectors, Options{Timeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(exp)
	return reg
}

// stripDurations removes the timing metrics, which are not deterministic.
func gatherText(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	for _, mf := range families {
		if mf.GetName() == "unraid_exporter_collector_duration_seconds" {
			continue
		}
		if err := writeFamily(&sb, mf); err != nil {
			t.Fatal(err)
		}
	}
	return sb.String()
}

func TestCollectorsGolden(t *testing.T) {
	for _, name := range Available() {
		t.Run(name, func(t *testing.T) {
			reg := newTestExporter(t, []string{name})
			got := gatherText(t, reg)
			golden := filepath.Join("testdata", "golden", name+".prom")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden file (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("metrics mismatch for %s (run with -update to accept)\n--- got ---\n%s", name, got)
			}
			if !strings.Contains(got, `unraid_exporter_collector_success{collector="`+name+`"} 1`) {
				t.Errorf("collector %s did not report success", name)
			}
		})
	}
}

func TestLint(t *testing.T) {
	reg := newTestExporter(t, Available())
	problems, err := testutil.GatherAndLint(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("lint %s: %s", p.Metric, p.Text)
	}
}

func TestCollectorFailureIsIsolated(t *testing.T) {
	dir := t.TempDir()
	// Only the info fixture exists: every other collector gets a GraphQL error.
	b, err := os.ReadFile(filepath.Join("testdata", "UnraidExporterInfo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "UnraidExporterInfo.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mockunraid.Handler(dir, testKey))
	defer srv.Close()
	client, _ := unraid.NewClient(unraid.Options{URL: srv.URL, APIKey: testKey, Timeout: 5 * time.Second})
	exp, err := New(client, []string{"info", "vms"}, Options{Timeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(exp)
	got := gatherText(t, reg)
	for _, want := range []string{
		`unraid_exporter_collector_success{collector="info"} 1`,
		`unraid_exporter_collector_success{collector="vms"} 0`,
		`unraid_up 1`,
		`unraid_info{`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(mockunraid.Handler("testdata", testKey))
	defer srv.Close()
	client, _ := unraid.NewClient(unraid.Options{URL: srv.URL, APIKey: "wrong", Timeout: 5 * time.Second})
	exp, err := New(client, []string{"info"}, Options{Timeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(exp)
	got := gatherText(t, reg)
	if !strings.Contains(got, "unraid_up 0") {
		t.Errorf("expected unraid_up 0, got:\n%s", got)
	}
}

func TestUnknownCollector(t *testing.T) {
	if _, err := New(nil, []string{"nope"}, Options{Timeout: time.Second}); err == nil {
		t.Fatal("expected error for unknown collector")
	}
}

// TestQueriesMatchSchema validates every query against the Unraid API schema
// (testdata/schema.graphql, from github.com/unraid/api generated-schema.graphql).
func TestQueriesMatchSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "schema.graphql"))
	if err != nil {
		t.Fatal(err)
	}
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: string(src)})
	if gqlErr != nil {
		t.Fatalf("loading schema: %v", gqlErr)
	}
	queries := map[string]string{
		"info": infoQuery, "array": arrayQuery, "metrics": metricsQuery, "temperature": temperatureQuery,
		"network": networkQuery, "docker": dockerQuery, "vms": vmsQuery, "shares": sharesQuery,
		"notifications": notificationsQuery, "ups": upsQuery, "disks": disksQuery,
	}
	for _, name := range Available() {
		if _, ok := queries[name]; !ok {
			t.Errorf("collector %s has no query registered in this test", name)
		}
	}
	for name, q := range queries {
		if _, errs := gqlparser.LoadQueryWithRules(schema, q, rules.NewDefaultRules()); len(errs) > 0 {
			t.Errorf("%s query invalid: %v", name, errs)
		}
	}
}

func TestNetworkExclude(t *testing.T) {
	srv := httptest.NewServer(mockunraid.Handler("testdata", testKey))
	defer srv.Close()
	client, _ := unraid.NewClient(unraid.Options{URL: srv.URL, APIKey: testKey, Timeout: 5 * time.Second})
	exp, err := New(client, []string{"network"}, Options{Timeout: 5 * time.Second, NetworkExclude: regexp.MustCompile(`^eth1$`)})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(exp)
	got := gatherText(t, reg)
	if strings.Contains(got, `interface="eth1"`) || !strings.Contains(got, `interface="eth0"`) {
		t.Errorf("exclude not applied:\n%s", got)
	}
}

func TestShortContainerID(t *testing.T) {
	for in, want := range map[string]string{
		"b25ca0db:255500f6b71f46fa7598319d0a43": "255500f6b71f",
		"docker:abc123":                         "abc123",
		"plainid":                               "plainid",
	} {
		if got := shortContainerID(in); got != want {
			t.Errorf("shortContainerID(%q) = %q, want %q", in, got, want)
		}
	}
}
