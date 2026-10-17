// Package collector exposes Unraid API data as Prometheus metrics.
package collector

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

const namespace = "unraid"

// Querier is the subset of the Unraid client used by collectors.
type Querier interface {
	Query(ctx context.Context, operationName, query string, vars map[string]any, out any) error
}

// subCollector scrapes one area of the Unraid API with a single GraphQL query.
type subCollector interface {
	Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error
}

// Options configures an Exporter.
type Options struct {
	// Timeout bounds a whole scrape of the Unraid API.
	Timeout time.Duration
	Logger  *slog.Logger
	// NetworkInclude keeps only network interfaces whose name matches (nil keeps all).
	NetworkInclude *regexp.Regexp
	// NetworkExclude drops network interfaces whose name matches (nil keeps all).
	NetworkExclude *regexp.Regexp
}

type factory struct {
	newFn          func(Options) subCollector
	defaultEnabled bool
}

var factories = map[string]factory{}

func register(name string, defaultEnabled bool, newFn func(Options) subCollector) {
	factories[name] = factory{newFn: newFn, defaultEnabled: defaultEnabled}
}

// Available returns all collector names, sorted.
func Available() []string {
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Defaults returns the names of collectors that are enabled by default, sorted.
func Defaults() []string {
	var names []string
	for n, f := range factories {
		if f.defaultEnabled {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

var (
	upDesc = prometheus.NewDesc(namespace+"_up",
		"Whether the Unraid API could be reached (1 if at least one collector succeeded).", nil, nil)
	successDesc = prometheus.NewDesc(namespace+"_exporter_collector_success",
		"Whether the collector succeeded.", []string{"collector"}, nil)
	durationDesc = prometheus.NewDesc(namespace+"_exporter_collector_duration_seconds",
		"Duration of the collector scrape.", []string{"collector"}, nil)
)

// Exporter is a prometheus.Collector running the enabled sub-collectors in parallel.
type Exporter struct {
	q          Querier
	timeout    time.Duration
	logger     *slog.Logger
	collectors map[string]subCollector
}

// New builds an Exporter with the given collector names enabled.
func New(q Querier, enabled []string, opts Options) (*Exporter, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	cs := make(map[string]subCollector, len(enabled))
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		f, ok := factories[name]
		if !ok {
			return nil, fmt.Errorf("unknown collector %q (available: %s)", name, strings.Join(Available(), ","))
		}
		cs[name] = f.newFn(opts)
	}
	if len(cs) == 0 {
		return nil, fmt.Errorf("no collectors enabled")
	}
	return &Exporter{q: q, timeout: opts.Timeout, logger: opts.Logger, collectors: cs}, nil
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- upDesc
	ch <- successDesc
	ch <- durationDesc
	for _, d := range allDescs {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		anyOK bool
	)
	for name, c := range e.collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := e.run(ctx, name, c, ch)
			if ok {
				mu.Lock()
				anyOK = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, boolToFloat(anyOK))
}

func (e *Exporter) run(ctx context.Context, name string, c subCollector, ch chan<- prometheus.Metric) bool {
	start := time.Now()
	err := c.Update(ctx, e.q, ch)
	duration := time.Since(start).Seconds()

	ok := err == nil
	if err != nil {
		if unraid.IsPartial(err) {
			// Data was decoded; some nullable fields failed (e.g. NaN disk temps).
			e.logger.Debug("collector returned partial data", "collector", name, "err", err)
			ok = true
		} else {
			e.logger.Error("collector failed", "collector", name, "duration_seconds", duration, "err", err)
		}
	}
	ch <- prometheus.MustNewConstMetric(successDesc, prometheus.GaugeValue, boolToFloat(ok), name)
	ch <- prometheus.MustNewConstMetric(durationDesc, prometheus.GaugeValue, duration, name)
	return ok
}

// fetch runs a query. It returns a non-nil error only if no data is usable;
// partial errors are returned via partial so the collector can still emit.
func fetch(ctx context.Context, q Querier, op, gql string, out any) (partial, err error) {
	err = q.Query(ctx, op, gql, nil, out)
	if err != nil && unraid.IsPartial(err) {
		return err, nil
	}
	return nil, err
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// allDescs holds every descriptor created by newDesc, for Describe.
var allDescs []*prometheus.Desc

func newDesc(subsystem, name, help string, labels ...string) *prometheus.Desc {
	d := prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labels, nil)
	allDescs = append(allDescs, d)
	return d
}

// emit sends a gauge if the value is valid.
func emit(ch chan<- prometheus.Metric, d *prometheus.Desc, t prometheus.ValueType, n unraid.Number, labels ...string) {
	if !n.Valid {
		return
	}
	ch <- prometheus.MustNewConstMetric(d, t, n.Value, labels...)
}

func emitScaled(ch chan<- prometheus.Metric, d *prometheus.Desc, t prometheus.ValueType, n unraid.Number, scale float64, labels ...string) {
	if !n.Valid {
		return
	}
	ch <- prometheus.MustNewConstMetric(d, t, n.Value*scale, labels...)
}

// emitPercent emits a 0-100 percentage as a 0-1 ratio.
func emitPercent(ch chan<- prometheus.Metric, d *prometheus.Desc, t prometheus.ValueType, n unraid.Number, labels ...string) {
	if !n.Valid {
		return
	}
	ch <- prometheus.MustNewConstMetric(d, t, n.Value/100, labels...)
}

// emitStateSet emits one series per possible state, 1 for the current state.
func emitStateSet(ch chan<- prometheus.Metric, d *prometheus.Desc, states []string, current string, labels ...string) {
	known := false
	for _, s := range states {
		if s == current {
			known = true
		}
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, boolToFloat(s == current), append(labels, s)...)
	}
	if !known && current != "" {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, 1, append(labels, current)...)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) unraid.Number {
	if b == nil {
		return unraid.Number{}
	}
	return unraid.Number{Value: boolToFloat(*b), Valid: true}
}

func num(v float64) unraid.Number { return unraid.Number{Value: v, Valid: true} }
