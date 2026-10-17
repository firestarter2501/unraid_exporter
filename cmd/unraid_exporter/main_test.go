package main

import (
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every configuration variable for the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, e := range envVars {
		t.Setenv(e.env, "") // registers restore
		_ = os.Unsetenv(e.env)
	}
}

func TestEveryFlagHasEnvVar(t *testing.T) {
	var ne, ll string
	fs := newFlagSet(&config{}, &ne, &ll)
	mapped := map[string]bool{}
	for _, e := range envVars {
		mapped[e.flag] = true
	}
	// Flags that trigger an action rather than configure the exporter.
	actions := map[string]bool{"version": true, "healthcheck": true}
	fs.VisitAll(func(f *flag.Flag) {
		if !mapped[f.Name] && !actions[f.Name] {
			t.Errorf("flag --%s has no environment variable", f.Name)
		}
	})
}

func TestParseEnvOnly(t *testing.T) {
	clearEnv(t)
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"UNRAID_URL":                      "http://tower",
		"UNRAID_API_KEY_FILE":             keyFile,
		"UNRAID_INSECURE_SKIP_VERIFY":     "true",
		"UNRAID_TIMEOUT":                  "3s",
		"UNRAID_EXPORTER_COLLECTORS":      "info,array",
		"UNRAID_EXPORTER_NETWORK_EXCLUDE": "^eth9$",
		"UNRAID_EXPORTER_LOG_LEVEL":       "debug",
		"UNRAID_EXPORTER_LISTEN_ADDRESS":  ":9999",
		"UNRAID_EXPORTER_TELEMETRY_PATH":  "/m",
	} {
		t.Setenv(k, v)
	}
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.unraidURL != "http://tower" || cfg.apiKey != "secret" || !cfg.insecure || cfg.timeout != 3*time.Second ||
		cfg.collectors != "info,array" || cfg.netExclude.String() != "^eth9$" || cfg.logLevel != slog.LevelDebug ||
		cfg.listenAddress != ":9999" || cfg.metricsPath != "/m" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestParseDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("UNRAID_URL", "http://tower")
	t.Setenv("UNRAID_API_KEY", "k")
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listenAddress != ":9703" || cfg.timeout != 10*time.Second || cfg.logLevel != slog.LevelInfo ||
		!cfg.netExclude.MatchString("veth1234") || cfg.netExclude.MatchString("br0") {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("UNRAID_URL", "http://from-env")
	t.Setenv("UNRAID_API_KEY", "k")
	cfg, err := parseFlags([]string{"--unraid.url=http://from-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.unraidURL != "http://from-flag" {
		t.Errorf("flag should win over env, got %q", cfg.unraidURL)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing url", map[string]string{"UNRAID_API_KEY": "k"}, "UNRAID_URL"},
		{"missing key", map[string]string{"UNRAID_URL": "http://t"}, "API key"},
		{"bad bool", map[string]string{"UNRAID_INSECURE_SKIP_VERIFY": "maybe"}, "UNRAID_INSECURE_SKIP_VERIFY"},
		{"bad duration", map[string]string{"UNRAID_TIMEOUT": "10"}, "UNRAID_TIMEOUT"},
		{"zero duration", map[string]string{"UNRAID_TIMEOUT": "0s"}, "UNRAID_TIMEOUT"},
		{"bad level", map[string]string{"UNRAID_EXPORTER_LOG_LEVEL": "loud"}, "UNRAID_EXPORTER_LOG_LEVEL"},
		{"bad regexp", map[string]string{"UNRAID_EXPORTER_NETWORK_EXCLUDE": "("}, "UNRAID_EXPORTER_NETWORK_EXCLUDE"},
		{"missing key file", map[string]string{"UNRAID_API_KEY_FILE": "/nonexistent"}, "UNRAID_API_KEY_FILE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			base := map[string]string{"UNRAID_URL": "http://t", "UNRAID_API_KEY": "k"}
			if c.name == "missing url" {
				delete(base, "UNRAID_URL")
			}
			if c.name == "missing key" {
				delete(base, "UNRAID_API_KEY")
			}
			for k, v := range base {
				t.Setenv(k, v)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			_, err := parseFlags(nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("expected error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/healthy" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := healthcheck(addr); err != nil {
		t.Errorf("healthy server: %v", err)
	}
	_, port, _ := strings.Cut(addr, ":")
	if err := healthcheck(":" + port); err != nil {
		t.Errorf("wildcard listen address: %v", err)
	}
	srv.Close()
	if err := healthcheck(addr); err == nil {
		t.Error("expected failure for closed server")
	}
}

func TestEmptyEnvUsesDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("UNRAID_URL", "http://tower")
	t.Setenv("UNRAID_API_KEY", "k")
	// Unraid passes blank template fields as empty variables.
	for _, e := range envVars {
		if e.env != "UNRAID_URL" && e.env != "UNRAID_API_KEY" {
			t.Setenv(e.env, "")
		}
	}
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.timeout != 10*time.Second || cfg.netExclude == nil || cfg.netExclude.String() != "^(lo|veth.*)$" || cfg.listenAddress != ":9703" {
		t.Errorf("empty variables should fall back to defaults: %+v", cfg)
	}

	t.Setenv("UNRAID_EXPORTER_NETWORK_EXCLUDE", "^$")
	cfg, err = parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.netExclude.MatchString("veth1") || cfg.netExclude.MatchString("lo") {
		t.Errorf("^$ should keep all interfaces")
	}
}
