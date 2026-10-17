// Command unraid_exporter exposes Unraid API (GraphQL) data as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"

	"github.com/firestarter2501/unraid_exporter/internal/collector"
	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

type config struct {
	listenAddress string
	metricsPath   string
	unraidURL     string
	apiKey        string
	apiKeyFile    string
	insecure      bool
	timeout       time.Duration
	collectors    string
	netExclude    *regexp.Regexp
	logLevel      slog.Level
	showVersion   bool
	healthcheck   bool
}

// envVars maps every configuration flag to its environment variable. An
// environment variable is applied unless the flag is given on the command
// line, and is validated by the same parser as the flag.
var envVars = []struct{ flag, env string }{
	{"web.listen-address", "UNRAID_EXPORTER_LISTEN_ADDRESS"},
	{"web.telemetry-path", "UNRAID_EXPORTER_TELEMETRY_PATH"},
	{"unraid.url", "UNRAID_URL"},
	{"unraid.api-key", "UNRAID_API_KEY"},
	{"unraid.api-key-file", "UNRAID_API_KEY_FILE"},
	{"unraid.insecure-skip-verify", "UNRAID_INSECURE_SKIP_VERIFY"},
	{"unraid.timeout", "UNRAID_TIMEOUT"},
	{"collectors", "UNRAID_EXPORTER_COLLECTORS"},
	{"collector.network.exclude", "UNRAID_EXPORTER_NETWORK_EXCLUDE"},
	{"log.level", "UNRAID_EXPORTER_LOG_LEVEL"},
}

// newFlagSet defines all flags. Raw string values that need further
// validation are written to netExclude and logLevel.
func newFlagSet(c *config, netExclude, logLevel *string) *flag.FlagSet {
	fs := flag.NewFlagSet("unraid_exporter", flag.ContinueOnError)
	fs.StringVar(&c.listenAddress, "web.listen-address", ":9703", "Address to listen on.")
	fs.StringVar(&c.metricsPath, "web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	fs.StringVar(&c.unraidURL, "unraid.url", "", "Base URL of the Unraid server, e.g. http://192.168.1.10 (required).")
	fs.StringVar(&c.apiKey, "unraid.api-key", "", "Unraid API key.")
	fs.StringVar(&c.apiKeyFile, "unraid.api-key-file", "", "File containing the Unraid API key (takes precedence over the key itself).")
	fs.BoolVar(&c.insecure, "unraid.insecure-skip-verify", false, "Skip TLS certificate verification.")
	fs.DurationVar(&c.timeout, "unraid.timeout", 10*time.Second, "Timeout for a scrape of the Unraid API.")
	fs.StringVar(&c.collectors, "collectors", strings.Join(collector.Defaults(), ","),
		"Comma-separated collectors to enable (available: "+strings.Join(collector.Available(), ",")+").")
	fs.StringVar(netExclude, "collector.network.exclude", "^(lo|veth.*)$", "Regexp of network interfaces to ignore (empty keeps all).")
	fs.StringVar(logLevel, "log.level", "info", "Log level: debug, info, warn, error.")
	fs.BoolVar(&c.showVersion, "version", false, "Print version and exit.")
	fs.BoolVar(&c.healthcheck, "healthcheck", false, "Check that the exporter listening on --web.listen-address is healthy and exit (for Docker HEALTHCHECK).")
	for _, e := range envVars {
		f := fs.Lookup(e.flag)
		f.Usage += " Env: " + e.env
	}
	return fs
}

func parseFlags(args []string) (*config, error) {
	c := &config{}
	var netExclude, logLevel string
	fs := newFlagSet(c, &netExclude, &logLevel)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	onCLI := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { onCLI[f.Name] = true })
	for _, e := range envVars {
		v, ok := os.LookupEnv(e.env)
		if !ok || onCLI[e.flag] {
			continue
		}
		if err := fs.Set(e.flag, v); err != nil {
			return nil, fmt.Errorf("invalid %s=%q: %w", e.env, v, err)
		}
	}
	if c.showVersion || c.healthcheck {
		return c, nil
	}

	if err := c.logLevel.UnmarshalText([]byte(logLevel)); err != nil {
		return nil, fmt.Errorf("invalid log level %q (UNRAID_EXPORTER_LOG_LEVEL): %w", logLevel, err)
	}
	if netExclude != "" {
		re, err := regexp.Compile(netExclude)
		if err != nil {
			return nil, fmt.Errorf("invalid network exclude regexp (UNRAID_EXPORTER_NETWORK_EXCLUDE): %w", err)
		}
		c.netExclude = re
	}
	if c.timeout <= 0 {
		return nil, errors.New("timeout (UNRAID_TIMEOUT) must be positive")
	}
	if c.apiKeyFile != "" {
		b, err := os.ReadFile(c.apiKeyFile)
		if err != nil {
			return nil, fmt.Errorf("reading API key file (UNRAID_API_KEY_FILE): %w", err)
		}
		c.apiKey = strings.TrimSpace(string(b))
	}
	if c.unraidURL == "" {
		return nil, errors.New("UNRAID_URL (--unraid.url) is required")
	}
	if c.apiKey == "" {
		return nil, errors.New("an API key is required: set UNRAID_API_KEY or UNRAID_API_KEY_FILE")
	}
	return c, nil
}

// healthcheck queries /-/healthy on the local listen address.
func healthcheck(listenAddress string) error {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/-/healthy")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

func newLogger(l slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

const landingPage = `<!doctype html>
<html><head><title>Unraid Exporter</title></head>
<body><h1>Unraid Exporter</h1><p><a href="%s">Metrics</a></p><p>%s</p></body></html>`

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if cfg.showVersion {
		fmt.Println(version.Print("unraid_exporter"))
		return
	}
	if cfg.healthcheck {
		if err := healthcheck(cfg.listenAddress); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}
	logger := newLogger(cfg.logLevel)

	client, err := unraid.NewClient(unraid.Options{
		URL:                cfg.unraidURL,
		APIKey:             cfg.apiKey,
		Timeout:            cfg.timeout,
		InsecureSkipVerify: cfg.insecure,
	})
	if err != nil {
		logger.Error("creating Unraid client", "err", err)
		os.Exit(2)
	}
	opts := collector.Options{Timeout: cfg.timeout, Logger: logger, NetworkExclude: cfg.netExclude}
	exp, err := collector.New(client, strings.Split(cfg.collectors, ","), opts)
	if err != nil {
		logger.Error("creating exporter", "err", err)
		os.Exit(2)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(exp, versioncollector.NewCollector("unraid_exporter"))

	mux := http.NewServeMux()
	mux.Handle(cfg.metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}))
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK\n")) })
	landing := []byte(fmt.Sprintf(landingPage, html.EscapeString(cfg.metricsPath), html.EscapeString(version.Info())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(landing)
	})

	srv := &http.Server{Addr: cfg.listenAddress, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting unraid_exporter", "version", version.Info(), "listen", cfg.listenAddress,
		"unraid_url", cfg.unraidURL, "collectors", cfg.collectors)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "err", err)
		os.Exit(1)
	}
}
