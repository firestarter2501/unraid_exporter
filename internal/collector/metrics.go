package collector

import (
	"context"
	"regexp"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

func init() {
	register("metrics", true, func(Options) subCollector { return &metricsCollector{} })
	register("temperature", true, func(Options) subCollector { return &temperatureCollector{} })
	register("network", true, func(o Options) subCollector { return &networkCollector{exclude: o.NetworkExclude} })
}

const metricsQuery = `query UnraidExporterMetrics {
  metrics {
    cpu {
      percentTotal
      cpus { percentTotal percentUser percentSystem percentNice percentIdle percentIrq percentGuest percentSteal }
    }
    memory {
      total used free available active buffcache percentTotal swapTotal swapUsed swapFree percentSwapTotal
    }
  }
}`

var (
	cpuUtilDesc     = newDesc("cpu", "utilization_ratio", "Overall CPU utilization.")
	cpuCoreUtilDesc = newDesc("cpu", "core_utilization_ratio", "Per-CPU utilization by mode.", "cpu", "mode")

	memTotalDesc     = newDesc("memory", "total_bytes", "Total memory.")
	memUsedDesc      = newDesc("memory", "used_bytes", "Used memory.")
	memFreeDesc      = newDesc("memory", "free_bytes", "Free memory.")
	memAvailableDesc = newDesc("memory", "available_bytes", "Available memory.")
	memActiveDesc    = newDesc("memory", "active_bytes", "Active memory.")
	memBuffcacheDesc = newDesc("memory", "buffcache_bytes", "Buffer and cache memory.")
	memUtilDesc      = newDesc("memory", "utilization_ratio", "Memory utilization.")
	swapTotalDesc    = newDesc("swap", "total_bytes", "Total swap.")
	swapUsedDesc     = newDesc("swap", "used_bytes", "Used swap.")
	swapFreeDesc     = newDesc("swap", "free_bytes", "Free swap.")
	swapUtilDesc     = newDesc("swap", "utilization_ratio", "Swap utilization.")
)

type cpuLoad struct {
	PercentTotal  unraid.Number `json:"percentTotal"`
	PercentUser   unraid.Number `json:"percentUser"`
	PercentSystem unraid.Number `json:"percentSystem"`
	PercentNice   unraid.Number `json:"percentNice"`
	PercentIdle   unraid.Number `json:"percentIdle"`
	PercentIrq    unraid.Number `json:"percentIrq"`
	PercentGuest  unraid.Number `json:"percentGuest"`
	PercentSteal  unraid.Number `json:"percentSteal"`
}

type metricsResponse struct {
	Metrics struct {
		CPU *struct {
			PercentTotal unraid.Number `json:"percentTotal"`
			CPUs         []cpuLoad     `json:"cpus"`
		} `json:"cpu"`
		Memory *struct {
			Total            unraid.Number `json:"total"`
			Used             unraid.Number `json:"used"`
			Free             unraid.Number `json:"free"`
			Available        unraid.Number `json:"available"`
			Active           unraid.Number `json:"active"`
			Buffcache        unraid.Number `json:"buffcache"`
			PercentTotal     unraid.Number `json:"percentTotal"`
			SwapTotal        unraid.Number `json:"swapTotal"`
			SwapUsed         unraid.Number `json:"swapUsed"`
			SwapFree         unraid.Number `json:"swapFree"`
			PercentSwapTotal unraid.Number `json:"percentSwapTotal"`
		} `json:"memory"`
	} `json:"metrics"`
}

type metricsCollector struct{}

func (c *metricsCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r metricsResponse
	partial, err := fetch(ctx, q, "UnraidExporterMetrics", metricsQuery, &r)
	if err != nil {
		return err
	}
	if cpu := r.Metrics.CPU; cpu != nil {
		emitPercent(ch, cpuUtilDesc, prometheus.GaugeValue, cpu.PercentTotal)
		for i, l := range cpu.CPUs {
			id := strconv.Itoa(i)
			for _, m := range []struct {
				mode string
				v    unraid.Number
			}{
				{"user", l.PercentUser}, {"system", l.PercentSystem}, {"nice", l.PercentNice},
				{"idle", l.PercentIdle}, {"irq", l.PercentIrq}, {"guest", l.PercentGuest}, {"steal", l.PercentSteal},
			} {
				emitPercent(ch, cpuCoreUtilDesc, prometheus.GaugeValue, m.v, id, m.mode)
			}
		}
	}
	if m := r.Metrics.Memory; m != nil {
		emit(ch, memTotalDesc, prometheus.GaugeValue, m.Total)
		emit(ch, memUsedDesc, prometheus.GaugeValue, m.Used)
		emit(ch, memFreeDesc, prometheus.GaugeValue, m.Free)
		emit(ch, memAvailableDesc, prometheus.GaugeValue, m.Available)
		emit(ch, memActiveDesc, prometheus.GaugeValue, m.Active)
		emit(ch, memBuffcacheDesc, prometheus.GaugeValue, m.Buffcache)
		emitPercent(ch, memUtilDesc, prometheus.GaugeValue, m.PercentTotal)
		emit(ch, swapTotalDesc, prometheus.GaugeValue, m.SwapTotal)
		emit(ch, swapUsedDesc, prometheus.GaugeValue, m.SwapUsed)
		emit(ch, swapFreeDesc, prometheus.GaugeValue, m.SwapFree)
		emitPercent(ch, swapUtilDesc, prometheus.GaugeValue, m.PercentSwapTotal)
	}
	return partial
}

// Temperature and network metrics were added in later Unraid API releases, so
// they use separate queries: older servers only fail these collectors.

const temperatureQuery = `query UnraidExporterTemperature {
  metrics {
    temperature {
      sensors {
        id name type location warning critical
        current { value unit status }
      }
    }
  }
}`

var (
	tempLabels       = []string{"sensor", "name", "type", "location"}
	tempDesc         = newDesc("temperature", "celsius", "Sensor temperature.", tempLabels...)
	tempWarnDesc     = newDesc("temperature", "warning_celsius", "Sensor warning threshold.", tempLabels...)
	tempCritDesc     = newDesc("temperature", "critical_celsius", "Sensor critical threshold.", tempLabels...)
	tempStatusDesc   = newDesc("temperature", "status", "Sensor status (1 for the current status).", append(tempLabels, "status")...)
	temperatureStats = []string{"NORMAL", "WARNING", "CRITICAL", "UNKNOWN"}
)

type temperatureResponse struct {
	Metrics struct {
		Temperature *struct {
			Sensors []struct {
				ID       string        `json:"id"`
				Name     string        `json:"name"`
				Type     string        `json:"type"`
				Location *string       `json:"location"`
				Warning  unraid.Number `json:"warning"`
				Critical unraid.Number `json:"critical"`
				Current  struct {
					Value  unraid.Number `json:"value"`
					Unit   string        `json:"unit"`
					Status string        `json:"status"`
				} `json:"current"`
			} `json:"sensors"`
		} `json:"temperature"`
	} `json:"metrics"`
}

type temperatureCollector struct{}

func (c *temperatureCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r temperatureResponse
	partial, err := fetch(ctx, q, "UnraidExporterTemperature", temperatureQuery, &r)
	if err != nil {
		return err
	}
	if r.Metrics.Temperature == nil {
		return partial
	}
	for _, s := range r.Metrics.Temperature.Sensors {
		lv := []string{s.ID, s.Name, s.Type, deref(s.Location)}
		unit := s.Current.Unit
		emit(ch, tempDesc, prometheus.GaugeValue, toCelsius(s.Current.Value, unit), lv...)
		emit(ch, tempWarnDesc, prometheus.GaugeValue, toCelsius(s.Warning, unit), lv...)
		emit(ch, tempCritDesc, prometheus.GaugeValue, toCelsius(s.Critical, unit), lv...)
		if s.Current.Status != "" {
			emitStateSet(ch, tempStatusDesc, temperatureStats, s.Current.Status, lv...)
		}
	}
	return partial
}

func toCelsius(n unraid.Number, unit string) unraid.Number {
	if !n.Valid {
		return n
	}
	switch unit {
	case "FAHRENHEIT":
		return num((n.Value - 32) * 5 / 9)
	case "KELVIN":
		return num(n.Value - 273.15)
	case "RANKINE":
		return num((n.Value - 491.67) * 5 / 9)
	default:
		return n
	}
}

const networkQuery = `query UnraidExporterNetwork {
  metrics {
    network {
      name operstate bytesReceived bytesSent packetsReceived packetsSent
      receiveErrors transmitErrors receiveDropped transmitDropped
    }
  }
}`

var (
	netUpDesc        = newDesc("network", "up", "Whether the interface operstate is up.", "interface", "operstate")
	netRxBytesDesc   = newDesc("network", "receive_bytes_total", "Bytes received.", "interface")
	netTxBytesDesc   = newDesc("network", "transmit_bytes_total", "Bytes transmitted.", "interface")
	netRxPacketsDesc = newDesc("network", "receive_packets_total", "Packets received.", "interface")
	netTxPacketsDesc = newDesc("network", "transmit_packets_total", "Packets transmitted.", "interface")
	netRxErrorsDesc  = newDesc("network", "receive_errors_total", "Receive errors.", "interface")
	netTxErrorsDesc  = newDesc("network", "transmit_errors_total", "Transmit errors.", "interface")
	netRxDroppedDesc = newDesc("network", "receive_dropped_total", "Received packets dropped.", "interface")
	netTxDroppedDesc = newDesc("network", "transmit_dropped_total", "Transmitted packets dropped.", "interface")
)

type networkResponse struct {
	Metrics struct {
		Network []struct {
			Name            string        `json:"name"`
			Operstate       *string       `json:"operstate"`
			BytesReceived   unraid.Number `json:"bytesReceived"`
			BytesSent       unraid.Number `json:"bytesSent"`
			PacketsReceived unraid.Number `json:"packetsReceived"`
			PacketsSent     unraid.Number `json:"packetsSent"`
			ReceiveErrors   unraid.Number `json:"receiveErrors"`
			TransmitErrors  unraid.Number `json:"transmitErrors"`
			ReceiveDropped  unraid.Number `json:"receiveDropped"`
			TransmitDropped unraid.Number `json:"transmitDropped"`
		} `json:"network"`
	} `json:"metrics"`
}

type networkCollector struct {
	exclude *regexp.Regexp
}

func (c *networkCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r networkResponse
	partial, err := fetch(ctx, q, "UnraidExporterNetwork", networkQuery, &r)
	if err != nil {
		return err
	}
	for _, n := range r.Metrics.Network {
		if c.exclude != nil && c.exclude.MatchString(n.Name) {
			continue
		}
		if n.Operstate != nil {
			ch <- prometheus.MustNewConstMetric(netUpDesc, prometheus.GaugeValue, boolToFloat(*n.Operstate == "up"), n.Name, *n.Operstate)
		}
		emit(ch, netRxBytesDesc, prometheus.CounterValue, n.BytesReceived, n.Name)
		emit(ch, netTxBytesDesc, prometheus.CounterValue, n.BytesSent, n.Name)
		emit(ch, netRxPacketsDesc, prometheus.CounterValue, n.PacketsReceived, n.Name)
		emit(ch, netTxPacketsDesc, prometheus.CounterValue, n.PacketsSent, n.Name)
		emit(ch, netRxErrorsDesc, prometheus.CounterValue, n.ReceiveErrors, n.Name)
		emit(ch, netTxErrorsDesc, prometheus.CounterValue, n.TransmitErrors, n.Name)
		emit(ch, netRxDroppedDesc, prometheus.CounterValue, n.ReceiveDropped, n.Name)
		emit(ch, netTxDroppedDesc, prometheus.CounterValue, n.TransmitDropped, n.Name)
	}
	return partial
}
