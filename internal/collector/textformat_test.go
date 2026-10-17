package collector

import (
	"io"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func writeFamily(w io.Writer, mf *dto.MetricFamily) error {
	_, err := expfmt.MetricFamilyToText(w, mf)
	return err
}
