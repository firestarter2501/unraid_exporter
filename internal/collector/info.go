package collector

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

func init() { register("info", true, func(Options) subCollector { return &infoCollector{} }) }

const infoQuery = `query UnraidExporterInfo {
  info {
    time
    os { hostname uptime kernel distro release arch }
    cpu { brand manufacturer cores threads speed speedmax }
    system { manufacturer model }
    versions { core { unraid api kernel } }
  }
}`

var (
	infoDesc = newDesc("", "info", "Unraid server information.",
		"hostname", "unraid_version", "api_version", "kernel", "arch")
	hardwareInfoDesc = newDesc("", "hardware_info", "Unraid server hardware information.",
		"system_manufacturer", "system_model", "cpu_manufacturer", "cpu_brand")
	bootTimeDesc   = newDesc("", "boot_time_seconds", "Unix time the server booted.")
	serverTimeDesc = newDesc("", "time_seconds", "Current server time reported by the Unraid API.")
	cpuCoresDesc   = newDesc("cpu", "cores", "Number of physical CPU cores.")
	cpuThreadsDesc = newDesc("cpu", "threads", "Number of CPU threads.")
	cpuSpeedDesc   = newDesc("cpu", "frequency_hertz", "CPU base frequency.")
	cpuMaxDesc     = newDesc("cpu", "frequency_max_hertz", "CPU maximum frequency.")
)

type infoCollector struct{}

type infoResponse struct {
	Info struct {
		Time *string `json:"time"`
		OS   struct {
			Hostname *string `json:"hostname"`
			Uptime   *string `json:"uptime"`
			Kernel   *string `json:"kernel"`
			Arch     *string `json:"arch"`
		} `json:"os"`
		CPU struct {
			Brand        *string       `json:"brand"`
			Manufacturer *string       `json:"manufacturer"`
			Cores        unraid.Number `json:"cores"`
			Threads      unraid.Number `json:"threads"`
			Speed        unraid.Number `json:"speed"`
			SpeedMax     unraid.Number `json:"speedmax"`
		} `json:"cpu"`
		System struct {
			Manufacturer *string `json:"manufacturer"`
			Model        *string `json:"model"`
		} `json:"system"`
		Versions struct {
			Core struct {
				Unraid *string `json:"unraid"`
				API    *string `json:"api"`
				Kernel *string `json:"kernel"`
			} `json:"core"`
		} `json:"versions"`
	} `json:"info"`
}

func (c *infoCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r infoResponse
	partial, err := fetch(ctx, q, "UnraidExporterInfo", infoQuery, &r)
	if err != nil {
		return err
	}
	i := r.Info

	kernel := deref(i.Versions.Core.Kernel)
	if kernel == "" {
		kernel = deref(i.OS.Kernel)
	}
	ch <- prometheus.MustNewConstMetric(infoDesc, prometheus.GaugeValue, 1,
		deref(i.OS.Hostname), deref(i.Versions.Core.Unraid), deref(i.Versions.Core.API), kernel, deref(i.OS.Arch))
	ch <- prometheus.MustNewConstMetric(hardwareInfoDesc, prometheus.GaugeValue, 1,
		deref(i.System.Manufacturer), deref(i.System.Model), deref(i.CPU.Manufacturer), deref(i.CPU.Brand))

	if t, ok := parseTime(i.OS.Uptime); ok {
		ch <- prometheus.MustNewConstMetric(bootTimeDesc, prometheus.GaugeValue, float64(t.UnixMilli())/1000)
	}
	if t, ok := parseTime(i.Time); ok {
		ch <- prometheus.MustNewConstMetric(serverTimeDesc, prometheus.GaugeValue, float64(t.UnixMilli())/1000)
	}
	emit(ch, cpuCoresDesc, prometheus.GaugeValue, i.CPU.Cores)
	emit(ch, cpuThreadsDesc, prometheus.GaugeValue, i.CPU.Threads)
	// systeminformation reports speeds in GHz.
	emitScaled(ch, cpuSpeedDesc, prometheus.GaugeValue, i.CPU.Speed, 1e9)
	emitScaled(ch, cpuMaxDesc, prometheus.GaugeValue, i.CPU.SpeedMax, 1e9)
	return partial
}

func parseTime(s *string) (time.Time, bool) {
	if s == nil || *s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
