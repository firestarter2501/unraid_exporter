package collector

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

func init() {
	register("docker", true, func(Options) subCollector { return &dockerCollector{} })
	register("vms", true, func(Options) subCollector { return &vmsCollector{} })
	register("shares", true, func(Options) subCollector { return &sharesCollector{} })
	register("notifications", true, func(Options) subCollector { return &notificationsCollector{} })
	register("ups", true, func(Options) subCollector { return &upsCollector{} })
	// Querying physical disks may make Unraid run smartctl, so it is opt-in.
	register("disks", false, func(Options) subCollector { return &disksCollector{} })
}

// --- docker ---------------------------------------------------------------

const dockerQuery = `query UnraidExporterDocker {
  docker {
    containers { id names image state created autoStart isUpdateAvailable }
  }
}`

var (
	containerStates        = []string{"RUNNING", "PAUSED", "EXITED"}
	containerInfoDesc      = newDesc("docker_container", "info", "Docker container information.", "name", "image", "id")
	containerStateDesc     = newDesc("docker_container", "state", "Docker container state (1 for the current state).", "name", "state")
	containerRunningDesc   = newDesc("docker_container", "running", "Whether the container is running.", "name")
	containerAutoStartDesc = newDesc("docker_container", "autostart", "Whether the container starts automatically.", "name")
	containerUpdateDesc    = newDesc("docker_container", "update_available", "Whether an image update is available.", "name")
	containerCreatedDesc   = newDesc("docker_container", "created_timestamp_seconds", "Unix time the container was created.", "name")
	dockerContainersDesc   = newDesc("docker", "containers", "Number of containers by state.", "state")
)

type dockerResponse struct {
	Docker struct {
		Containers []struct {
			ID                string        `json:"id"`
			Names             []string      `json:"names"`
			Image             string        `json:"image"`
			State             string        `json:"state"`
			Created           unraid.Number `json:"created"`
			AutoStart         *bool         `json:"autoStart"`
			IsUpdateAvailable *bool         `json:"isUpdateAvailable"`
		} `json:"containers"`
	} `json:"docker"`
}

type dockerCollector struct{}

func (c *dockerCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r dockerResponse
	partial, err := fetch(ctx, q, "UnraidExporterDocker", dockerQuery, &r)
	if err != nil {
		return err
	}
	counts := map[string]float64{}
	for _, s := range containerStates {
		counts[s] = 0
	}
	for _, ct := range r.Docker.Containers {
		name := ct.ID
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		counts[ct.State]++
		ch <- prometheus.MustNewConstMetric(containerInfoDesc, prometheus.GaugeValue, 1, name, ct.Image, shortContainerID(ct.ID))
		emitStateSet(ch, containerStateDesc, containerStates, ct.State, name)
		ch <- prometheus.MustNewConstMetric(containerRunningDesc, prometheus.GaugeValue, boolToFloat(ct.State == "RUNNING"), name)
		emit(ch, containerAutoStartDesc, prometheus.GaugeValue, derefBool(ct.AutoStart), name)
		emit(ch, containerUpdateDesc, prometheus.GaugeValue, derefBool(ct.IsUpdateAvailable), name)
		emit(ch, containerCreatedDesc, prometheus.GaugeValue, ct.Created, name)
	}
	for state, n := range counts {
		ch <- prometheus.MustNewConstMetric(dockerContainersDesc, prometheus.GaugeValue, n, state)
	}
	return partial
}

// shortContainerID strips the Unraid server prefix from a PrefixedID
// ("<server>:<container id>") and shortens it like the docker CLI does.
func shortContainerID(id string) string {
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

// --- vms ------------------------------------------------------------------

const vmsQuery = `query UnraidExporterVms {
  vms { domains { id name state } }
}`

var (
	vmStates      = []string{"NOSTATE", "RUNNING", "IDLE", "PAUSED", "SHUTDOWN", "SHUTOFF", "CRASHED", "PMSUSPENDED"}
	vmStateDesc   = newDesc("vm", "state", "VM state (1 for the current state).", "name", "state")
	vmRunningDesc = newDesc("vm", "running", "Whether the VM is running.", "name")
)

type vmsResponse struct {
	VMs struct {
		Domains []struct {
			ID    string  `json:"id"`
			Name  *string `json:"name"`
			State string  `json:"state"`
		} `json:"domains"`
	} `json:"vms"`
}

type vmsCollector struct{}

func (c *vmsCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r vmsResponse
	partial, err := fetch(ctx, q, "UnraidExporterVms", vmsQuery, &r)
	if err != nil {
		return err
	}
	for _, d := range r.VMs.Domains {
		name := deref(d.Name)
		if name == "" {
			name = d.ID
		}
		emitStateSet(ch, vmStateDesc, vmStates, d.State, name)
		ch <- prometheus.MustNewConstMetric(vmRunningDesc, prometheus.GaugeValue, boolToFloat(d.State == "RUNNING"), name)
	}
	return partial
}

// --- shares ---------------------------------------------------------------

const sharesQuery = `query UnraidExporterShares {
  shares { name free used size cache comment }
}`

var (
	shareFreeDesc = newDesc("share", "free_bytes", "Free space available to the share.", "name")
	shareUsedDesc = newDesc("share", "used_bytes", "Space used by the share.", "name")
	shareSizeDesc = newDesc("share", "size_bytes", "Share size as reported by Unraid.", "name")
)

type sharesResponse struct {
	Shares []struct {
		Name *string       `json:"name"`
		Free unraid.Number `json:"free"`
		Used unraid.Number `json:"used"`
		Size unraid.Number `json:"size"`
	} `json:"shares"`
}

type sharesCollector struct{}

func (c *sharesCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r sharesResponse
	partial, err := fetch(ctx, q, "UnraidExporterShares", sharesQuery, &r)
	if err != nil {
		return err
	}
	for _, s := range r.Shares {
		name := deref(s.Name)
		if name == "" {
			continue
		}
		emitScaled(ch, shareFreeDesc, prometheus.GaugeValue, s.Free, kib, name)
		emitScaled(ch, shareUsedDesc, prometheus.GaugeValue, s.Used, kib, name)
		emitScaled(ch, shareSizeDesc, prometheus.GaugeValue, s.Size, kib, name)
	}
	return partial
}

// --- notifications --------------------------------------------------------

const notificationsQuery = `query UnraidExporterNotifications {
  notifications {
    overview {
      unread { info warning alert total }
      archive { info warning alert total }
    }
  }
}`

var notificationsDesc = newDesc("", "notifications", "Number of notifications by folder and importance.", "folder", "importance")

type notificationCounts struct {
	Info    unraid.Number `json:"info"`
	Warning unraid.Number `json:"warning"`
	Alert   unraid.Number `json:"alert"`
}

type notificationsResponse struct {
	Notifications struct {
		Overview struct {
			Unread  notificationCounts `json:"unread"`
			Archive notificationCounts `json:"archive"`
		} `json:"overview"`
	} `json:"notifications"`
}

type notificationsCollector struct{}

func (c *notificationsCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r notificationsResponse
	partial, err := fetch(ctx, q, "UnraidExporterNotifications", notificationsQuery, &r)
	if err != nil {
		return err
	}
	for folder, nc := range map[string]notificationCounts{
		"unread":  r.Notifications.Overview.Unread,
		"archive": r.Notifications.Overview.Archive,
	} {
		emit(ch, notificationsDesc, prometheus.GaugeValue, nc.Info, folder, "info")
		emit(ch, notificationsDesc, prometheus.GaugeValue, nc.Warning, folder, "warning")
		emit(ch, notificationsDesc, prometheus.GaugeValue, nc.Alert, folder, "alert")
	}
	return partial
}

// --- ups ------------------------------------------------------------------

const upsQuery = `query UnraidExporterUps {
  upsDevices {
    id name model status
    battery { chargeLevel estimatedRuntime health }
    power { inputVoltage outputVoltage loadPercentage nominalPower currentPower }
  }
}`

var (
	upsLabels      = []string{"ups", "model"}
	upsInfoDesc    = newDesc("ups", "info", "UPS information.", "ups", "model", "status", "battery_health")
	upsOnlineDesc  = newDesc("ups", "online", "Whether the UPS is running on mains power.", upsLabels...)
	upsChargeDesc  = newDesc("ups", "battery_charge_ratio", "UPS battery charge.", upsLabels...)
	upsRuntimeDesc = newDesc("ups", "battery_runtime_seconds", "Estimated runtime on battery.", upsLabels...)
	upsInVoltDesc  = newDesc("ups", "input_voltage_volts", "UPS input voltage.", upsLabels...)
	upsOutVoltDesc = newDesc("ups", "output_voltage_volts", "UPS output voltage.", upsLabels...)
	upsLoadDesc    = newDesc("ups", "load_ratio", "UPS load relative to its capacity.", upsLabels...)
	upsNominalDesc = newDesc("ups", "nominal_power_watts", "UPS nominal power.", upsLabels...)
	upsPowerDesc   = newDesc("ups", "power_watts", "Current UPS power draw.", upsLabels...)
)

type upsResponse struct {
	UPSDevices []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Model   string `json:"model"`
		Status  string `json:"status"`
		Battery struct {
			ChargeLevel      unraid.Number `json:"chargeLevel"`
			EstimatedRuntime unraid.Number `json:"estimatedRuntime"`
			Health           string        `json:"health"`
		} `json:"battery"`
		Power struct {
			InputVoltage   unraid.Number `json:"inputVoltage"`
			OutputVoltage  unraid.Number `json:"outputVoltage"`
			LoadPercentage unraid.Number `json:"loadPercentage"`
			NominalPower   unraid.Number `json:"nominalPower"`
			CurrentPower   unraid.Number `json:"currentPower"`
		} `json:"power"`
	} `json:"upsDevices"`
}

type upsCollector struct{}

func (c *upsCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r upsResponse
	partial, err := fetch(ctx, q, "UnraidExporterUps", upsQuery, &r)
	if err != nil {
		return err
	}
	for _, u := range r.UPSDevices {
		name := u.Name
		if name == "" {
			name = u.ID
		}
		lv := []string{name, u.Model}
		ch <- prometheus.MustNewConstMetric(upsInfoDesc, prometheus.GaugeValue, 1, name, u.Model, u.Status, u.Battery.Health)
		ch <- prometheus.MustNewConstMetric(upsOnlineDesc, prometheus.GaugeValue, boolToFloat(isUPSOnline(u.Status)), lv...)
		emitPercent(ch, upsChargeDesc, prometheus.GaugeValue, u.Battery.ChargeLevel, lv...)
		emit(ch, upsRuntimeDesc, prometheus.GaugeValue, u.Battery.EstimatedRuntime, lv...)
		emit(ch, upsInVoltDesc, prometheus.GaugeValue, u.Power.InputVoltage, lv...)
		emit(ch, upsOutVoltDesc, prometheus.GaugeValue, u.Power.OutputVoltage, lv...)
		emitPercent(ch, upsLoadDesc, prometheus.GaugeValue, u.Power.LoadPercentage, lv...)
		emit(ch, upsNominalDesc, prometheus.GaugeValue, u.Power.NominalPower, lv...)
		emit(ch, upsPowerDesc, prometheus.GaugeValue, u.Power.CurrentPower, lv...)
	}
	return partial
}

// isUPSOnline accepts both the documented status ("Online") and apcupsd's
// raw form ("ONLINE"), but not combined states like "ONBATT".
func isUPSOnline(status string) bool {
	s := strings.ToUpper(strings.TrimSpace(status))
	return s == "ONLINE" || strings.HasPrefix(s, "ONLINE ")
}

// --- disks (physical) -----------------------------------------------------

const disksQuery = `query UnraidExporterDisks {
  disks { device name vendor serialNum type interfaceType size smartStatus temperature isSpinning }
}`

var (
	physLabels       = []string{"device"}
	physInfoDesc     = newDesc("disk", "info", "Physical disk information.", "device", "name", "vendor", "serial", "type", "interface")
	physSizeDesc     = newDesc("disk", "size_bytes", "Physical disk size.", physLabels...)
	physTempDesc     = newDesc("disk", "temperature_celsius", "Physical disk temperature.", physLabels...)
	physSmartDesc    = newDesc("disk", "smart_ok", "Whether the SMART status is OK.", physLabels...)
	physSpinningDesc = newDesc("disk", "spinning", "Whether the disk is spinning.", physLabels...)
)

type disksResponse struct {
	Disks []struct {
		Device        string        `json:"device"`
		Name          string        `json:"name"`
		Vendor        string        `json:"vendor"`
		SerialNum     string        `json:"serialNum"`
		Type          string        `json:"type"`
		InterfaceType string        `json:"interfaceType"`
		Size          unraid.Number `json:"size"`
		SmartStatus   string        `json:"smartStatus"`
		Temperature   unraid.Number `json:"temperature"`
		IsSpinning    *bool         `json:"isSpinning"`
	} `json:"disks"`
}

type disksCollector struct{}

func (c *disksCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r disksResponse
	partial, err := fetch(ctx, q, "UnraidExporterDisks", disksQuery, &r)
	if err != nil {
		return err
	}
	for _, d := range r.Disks {
		ch <- prometheus.MustNewConstMetric(physInfoDesc, prometheus.GaugeValue, 1,
			d.Device, d.Name, d.Vendor, d.SerialNum, d.Type, d.InterfaceType)
		emit(ch, physSizeDesc, prometheus.GaugeValue, d.Size, d.Device)
		emit(ch, physTempDesc, prometheus.GaugeValue, d.Temperature, d.Device)
		if d.SmartStatus != "" {
			ch <- prometheus.MustNewConstMetric(physSmartDesc, prometheus.GaugeValue, boolToFloat(d.SmartStatus == "OK"), d.Device)
		}
		emit(ch, physSpinningDesc, prometheus.GaugeValue, derefBool(d.IsSpinning), d.Device)
	}
	return partial
}
