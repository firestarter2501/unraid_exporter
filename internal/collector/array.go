package collector

import (
	"context"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/firestarter2501/unraid_exporter/internal/unraid"
)

func init() { register("array", true, func(Options) subCollector { return &arrayCollector{} }) }

const arrayQuery = `query UnraidExporterArray {
  array {
    state
    capacity {
      kilobytes { free used total }
      disks { free used total }
    }
    parityCheckStatus { date duration speed status errors progress correcting paused running }
    parities { ...ArrayDiskFields }
    disks { ...ArrayDiskFields }
    caches { ...ArrayDiskFields }
    boot { ...ArrayDiskFields }
  }
}

fragment ArrayDiskFields on ArrayDisk {
  id idx name device size status rotational temp numReads numWrites numErrors
  fsSize fsFree fsUsed type warning critical fsType transport color isSpinning
}`

var (
	arrayStates = []string{"STARTED", "STOPPED", "NEW_ARRAY", "RECON_DISK", "DISABLE_DISK", "SWAP_DSBL",
		"INVALID_EXPANSION", "PARITY_NOT_BIGGEST", "TOO_MANY_MISSING_DISKS", "NEW_DISK_TOO_SMALL", "NO_DATA_DISKS"}
	arrayDiskStatuses = []string{"DISK_NP", "DISK_OK", "DISK_NP_MISSING", "DISK_INVALID", "DISK_WRONG",
		"DISK_DSBL", "DISK_NP_DSBL", "DISK_DSBL_NEW", "DISK_NEW"}
	parityCheckStatuses = []string{"NEVER_RUN", "RUNNING", "PAUSED", "COMPLETED", "CANCELLED", "FAILED"}
)

var (
	arrayStateDesc   = newDesc("array", "state", "Array state (1 for the current state).", "state")
	arrayStartedDesc = newDesc("array", "started", "Whether the array is started.")
	arrayTotalDesc   = newDesc("array", "size_bytes", "Total array capacity.")
	arrayUsedDesc    = newDesc("array", "used_bytes", "Used array capacity.")
	arrayFreeDesc    = newDesc("array", "free_bytes", "Free array capacity.")
	arraySlotsDesc   = newDesc("array", "disk_slots", "Array disk slots by usage.", "usage")

	diskLabels       = []string{"name", "device", "type"}
	diskInfoDesc     = newDesc("array_disk", "info", "Array disk information. disk_id is Unraid's <model>_<serial> identifier.", "name", "device", "type", "idx", "disk_id", "fs_type", "transport", "rotational")
	diskStatusDesc   = newDesc("array_disk", "status", "Array disk status (1 for the current status).", append(diskLabels, "status")...)
	diskColorDesc    = newDesc("array_disk", "color_info", "Array disk status color as shown in the Unraid UI.", append(diskLabels, "color")...)
	diskSizeDesc     = newDesc("array_disk", "size_bytes", "Array disk size.", diskLabels...)
	diskFsSizeDesc   = newDesc("array_disk", "fs_size_bytes", "Array disk filesystem size.", diskLabels...)
	diskFsFreeDesc   = newDesc("array_disk", "fs_free_bytes", "Array disk filesystem free space.", diskLabels...)
	diskFsUsedDesc   = newDesc("array_disk", "fs_used_bytes", "Array disk filesystem used space.", diskLabels...)
	diskTempDesc     = newDesc("array_disk", "temperature_celsius", "Array disk temperature.", diskLabels...)
	diskReadsDesc    = newDesc("array_disk", "reads_total", "Read I/O requests (may be reset by Unraid).", diskLabels...)
	diskWritesDesc   = newDesc("array_disk", "writes_total", "Write I/O requests (may be reset by Unraid).", diskLabels...)
	diskErrorsDesc   = newDesc("array_disk", "errors_total", "Unrecoverable I/O errors.", diskLabels...)
	diskSpinningDesc = newDesc("array_disk", "spinning", "Whether the disk is spinning.", diskLabels...)
	diskWarnDesc     = newDesc("array_disk", "warning_threshold_ratio", "Disk utilization warning threshold.", diskLabels...)
	diskCritDesc     = newDesc("array_disk", "critical_threshold_ratio", "Disk utilization critical threshold.", diskLabels...)

	parityStatusDesc     = newDesc("parity_check", "status", "Parity check status (1 for the current status).", "status")
	parityRunningDesc    = newDesc("parity_check", "running", "Whether a parity check is running.")
	parityPausedDesc     = newDesc("parity_check", "paused", "Whether the parity check is paused.")
	parityCorrectingDesc = newDesc("parity_check", "correcting", "Whether the parity check is correcting.")
	parityProgressDesc   = newDesc("parity_check", "progress_ratio", "Parity check progress.")
	parityErrorsDesc     = newDesc("parity_check", "errors", "Errors found by the current or last parity check.")
	parityDurationDesc   = newDesc("parity_check", "duration_seconds", "Duration of the current or last parity check.")
	paritySpeedDesc      = newDesc("parity_check", "speed_bytes_per_second", "Speed of the current or last parity check.")
	parityDateDesc       = newDesc("parity_check", "timestamp_seconds", "Unix time of the current or last parity check.")
)

type arrayDisk struct {
	ID         string        `json:"id"`
	Idx        unraid.Number `json:"idx"`
	Name       *string       `json:"name"`
	Device     *string       `json:"device"`
	Size       unraid.Number `json:"size"`
	Status     *string       `json:"status"`
	Rotational *bool         `json:"rotational"`
	Temp       unraid.Number `json:"temp"`
	NumReads   unraid.Number `json:"numReads"`
	NumWrites  unraid.Number `json:"numWrites"`
	NumErrors  unraid.Number `json:"numErrors"`
	FsSize     unraid.Number `json:"fsSize"`
	FsFree     unraid.Number `json:"fsFree"`
	FsUsed     unraid.Number `json:"fsUsed"`
	Type       string        `json:"type"`
	Warning    unraid.Number `json:"warning"`
	Critical   unraid.Number `json:"critical"`
	FsType     *string       `json:"fsType"`
	Transport  *string       `json:"transport"`
	Color      *string       `json:"color"`
	IsSpinning *bool         `json:"isSpinning"`
}

type arrayResponse struct {
	Array struct {
		State    string `json:"state"`
		Capacity struct {
			Kilobytes struct {
				Free  unraid.Number `json:"free"`
				Used  unraid.Number `json:"used"`
				Total unraid.Number `json:"total"`
			} `json:"kilobytes"`
			Disks struct {
				Free  unraid.Number `json:"free"`
				Used  unraid.Number `json:"used"`
				Total unraid.Number `json:"total"`
			} `json:"disks"`
		} `json:"capacity"`
		ParityCheckStatus *struct {
			Date       *string       `json:"date"`
			Duration   unraid.Number `json:"duration"`
			Speed      unraid.Number `json:"speed"`
			Status     string        `json:"status"`
			Errors     unraid.Number `json:"errors"`
			Progress   unraid.Number `json:"progress"`
			Correcting *bool         `json:"correcting"`
			Paused     *bool         `json:"paused"`
			Running    *bool         `json:"running"`
		} `json:"parityCheckStatus"`
		Parities []arrayDisk `json:"parities"`
		Disks    []arrayDisk `json:"disks"`
		Caches   []arrayDisk `json:"caches"`
		Boot     *arrayDisk  `json:"boot"`
	} `json:"array"`
}

type arrayCollector struct{}

const kib = 1024

func (c *arrayCollector) Update(ctx context.Context, q Querier, ch chan<- prometheus.Metric) error {
	var r arrayResponse
	partial, err := fetch(ctx, q, "UnraidExporterArray", arrayQuery, &r)
	if err != nil {
		return err
	}
	a := r.Array

	emitStateSet(ch, arrayStateDesc, arrayStates, a.State)
	ch <- prometheus.MustNewConstMetric(arrayStartedDesc, prometheus.GaugeValue, boolToFloat(a.State == "STARTED"))
	emitScaled(ch, arrayTotalDesc, prometheus.GaugeValue, a.Capacity.Kilobytes.Total, kib)
	emitScaled(ch, arrayUsedDesc, prometheus.GaugeValue, a.Capacity.Kilobytes.Used, kib)
	emitScaled(ch, arrayFreeDesc, prometheus.GaugeValue, a.Capacity.Kilobytes.Free, kib)
	emit(ch, arraySlotsDesc, prometheus.GaugeValue, a.Capacity.Disks.Total, "total")
	emit(ch, arraySlotsDesc, prometheus.GaugeValue, a.Capacity.Disks.Used, "used")
	emit(ch, arraySlotsDesc, prometheus.GaugeValue, a.Capacity.Disks.Free, "free")

	if p := a.ParityCheckStatus; p != nil {
		emitStateSet(ch, parityStatusDesc, parityCheckStatuses, p.Status)
		emit(ch, parityRunningDesc, prometheus.GaugeValue, derefBool(p.Running))
		emit(ch, parityPausedDesc, prometheus.GaugeValue, derefBool(p.Paused))
		emit(ch, parityCorrectingDesc, prometheus.GaugeValue, derefBool(p.Correcting))
		emitPercent(ch, parityProgressDesc, prometheus.GaugeValue, p.Progress)
		emit(ch, parityErrorsDesc, prometheus.GaugeValue, p.Errors)
		emit(ch, parityDurationDesc, prometheus.GaugeValue, p.Duration)
		// Unraid reports the speed in MB/s.
		emitScaled(ch, paritySpeedDesc, prometheus.GaugeValue, p.Speed, 1e6)
		if t, ok := parseTime(p.Date); ok {
			ch <- prometheus.MustNewConstMetric(parityDateDesc, prometheus.GaugeValue, float64(t.Unix()))
		}
	}

	seen := map[string]bool{}
	disks := make([]arrayDisk, 0, len(a.Parities)+len(a.Disks)+len(a.Caches)+1)
	disks = append(disks, a.Parities...)
	disks = append(disks, a.Disks...)
	disks = append(disks, a.Caches...)
	if a.Boot != nil {
		disks = append(disks, *a.Boot)
	}
	for _, d := range disks {
		name := deref(d.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		emitArrayDisk(ch, d, name)
	}
	return partial
}

func emitArrayDisk(ch chan<- prometheus.Metric, d arrayDisk, name string) {
	lv := []string{name, deref(d.Device), strings.ToLower(d.Type)}
	idx := ""
	if d.Idx.Valid {
		idx = strconv.Itoa(int(d.Idx.Value))
	}
	rotational := ""
	if d.Rotational != nil {
		rotational = strconv.FormatBool(*d.Rotational)
	}
	ch <- prometheus.MustNewConstMetric(diskInfoDesc, prometheus.GaugeValue, 1,
		name, deref(d.Device), strings.ToLower(d.Type), idx, emhttpDiskID(d.ID), deref(d.FsType), deref(d.Transport), rotational)
	if d.Status != nil {
		emitStateSet(ch, diskStatusDesc, arrayDiskStatuses, *d.Status, lv...)
	}
	if d.Color != nil {
		ch <- prometheus.MustNewConstMetric(diskColorDesc, prometheus.GaugeValue, 1, append(lv, *d.Color)...)
	}
	emitScaled(ch, diskSizeDesc, prometheus.GaugeValue, d.Size, kib, lv...)
	emitScaled(ch, diskFsSizeDesc, prometheus.GaugeValue, d.FsSize, kib, lv...)
	emitScaled(ch, diskFsFreeDesc, prometheus.GaugeValue, d.FsFree, kib, lv...)
	emitScaled(ch, diskFsUsedDesc, prometheus.GaugeValue, d.FsUsed, kib, lv...)
	emit(ch, diskTempDesc, prometheus.GaugeValue, d.Temp, lv...)
	emit(ch, diskReadsDesc, prometheus.CounterValue, d.NumReads, lv...)
	emit(ch, diskWritesDesc, prometheus.CounterValue, d.NumWrites, lv...)
	emit(ch, diskErrorsDesc, prometheus.CounterValue, d.NumErrors, lv...)
	emit(ch, diskSpinningDesc, prometheus.GaugeValue, derefBool(d.IsSpinning), lv...)
	emitPercent(ch, diskWarnDesc, prometheus.GaugeValue, d.Warning, lv...)
	emitPercent(ch, diskCritDesc, prometheus.GaugeValue, d.Critical, lv...)
}

// emhttpDiskID strips the server prefix from an ArrayDisk PrefixedID
// ("<server>:<model>_<serial>"). It comes from emhttp's state, so unlike the
// "disks" query it never touches the drives.
func emhttpDiskID(id string) string {
	if _, after, ok := strings.Cut(id, ":"); ok {
		return after
	}
	return id
}
