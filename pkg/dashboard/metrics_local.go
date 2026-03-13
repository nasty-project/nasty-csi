package dashboard

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"k8s.io/klog/v2"
)

// GatherLocalMetrics reads metrics from prometheus.DefaultGatherer (in-process)
// instead of scraping an HTTP endpoint. This is the key difference from the
// kubectl plugin's fetchControllerMetrics which must proxy through the K8s API.
func GatherLocalMetrics() *MetricsSummary {
	summary := &MetricsSummary{}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		klog.V(4).Infof("Failed to gather metrics: %v", err)
		summary.Error = err.Error()
		return summary
	}

	for _, family := range families {
		name := family.GetName()

		switch name {
		case "nasty_csi_websocket_connection_status":
			for _, m := range family.GetMetric() {
				if m.GetGauge() != nil {
					summary.WebSocketConnected = m.GetGauge().GetValue() == 1
				}
			}

		case "nasty_csi_websocket_reconnections_total":
			for _, m := range family.GetMetric() {
				if m.GetCounter() != nil {
					summary.WebSocketReconnects = int64(m.GetCounter().GetValue())
				}
			}

		case "nasty_csi_websocket_connection_duration_seconds":
			for _, m := range family.GetMetric() {
				if m.GetGauge() != nil {
					summary.ConnectionDurationSecs = m.GetGauge().GetValue()
				}
			}

		case "nasty_csi_websocket_messages_total":
			gatherMessageMetrics(summary, family.GetMetric())

		case "nasty_csi_volume_operations_total":
			gatherOperationMetrics(summary, family.GetMetric())
		}
	}

	summary.TotalOperations = summary.SuccessOperations + summary.ErrorOperations

	return summary
}

func gatherMessageMetrics(summary *MetricsSummary, metrics []*dto.Metric) {
	for _, m := range metrics {
		if m.GetCounter() == nil {
			continue
		}
		val := int64(m.GetCounter().GetValue())
		switch getLabelValue(m, "direction") {
		case "sent":
			summary.MessagesSent = val
		case "received":
			summary.MessagesReceived = val
		}
	}
}

func gatherOperationMetrics(summary *MetricsSummary, metrics []*dto.Metric) {
	for _, m := range metrics {
		if m.GetCounter() == nil {
			continue
		}
		val := int64(m.GetCounter().GetValue())

		switch getLabelValue(m, "status") {
		case "success":
			summary.SuccessOperations += val
		case "error":
			summary.ErrorOperations += val
		}

		switch getLabelValue(m, "protocol") {
		case "nfs":
			summary.NFSOperations += val
		case "nvmeof":
			summary.NVMeOFOperations += val
		case "iscsi":
			summary.ISCSIOperations += val
		case "smb":
			summary.SMBOperations += val
		}

		switch getLabelValue(m, "operation") {
		case "create":
			summary.CreateOperations += val
		case "delete":
			summary.DeleteOperations += val
		case "expand":
			summary.ExpandOperations += val
		}
	}
}

// GatherRawMetrics returns raw Prometheus text format from in-process gatherer.
func GatherRawMetrics() (string, error) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, family := range families {
		if family.GetHelp() != "" {
			fmt.Fprintf(&sb, "# HELP %s %s\n", family.GetName(), family.GetHelp())
		}
		fmt.Fprintf(&sb, "# TYPE %s %s\n", family.GetName(), strings.ToLower(family.GetType().String()))

		for _, m := range family.GetMetric() {
			writeMetric(&sb, family.GetName(), family.GetType(), m)
		}
	}

	return sb.String(), nil
}

func writeMetric(sb *strings.Builder, name string, metricType dto.MetricType, m *dto.Metric) {
	labels := formatLabels(m)

	switch metricType {
	case dto.MetricType_COUNTER:
		if m.GetCounter() != nil {
			fmt.Fprintf(sb, "%s%s %s\n", name, labels, fmtFloat(m.GetCounter().GetValue()))
		}
	case dto.MetricType_GAUGE:
		if m.GetGauge() != nil {
			fmt.Fprintf(sb, "%s%s %s\n", name, labels, fmtFloat(m.GetGauge().GetValue()))
		}
	case dto.MetricType_HISTOGRAM:
		if h := m.GetHistogram(); h != nil {
			for _, b := range h.GetBucket() {
				fmt.Fprintf(sb, "%s_bucket%s %d\n", name,
					formatLabelsWithExtra(m, "le", fmtFloat(b.GetUpperBound())),
					b.GetCumulativeCount())
			}
			fmt.Fprintf(sb, "%s_sum%s %s\n", name, labels, fmtFloat(h.GetSampleSum()))
			fmt.Fprintf(sb, "%s_count%s %d\n", name, labels, h.GetSampleCount())
		}
	default:
		if m.GetUntyped() != nil {
			fmt.Fprintf(sb, "%s%s %s\n", name, labels, fmtFloat(m.GetUntyped().GetValue()))
		}
	}
}

func getLabelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func formatLabels(m *dto.Metric) string {
	labels := m.GetLabel()
	if len(labels) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("{")
	for i, l := range labels {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "%s=%q", l.GetName(), l.GetValue())
	}
	sb.WriteString("}")
	return sb.String()
}

func formatLabelsWithExtra(m *dto.Metric, extraName, extraValue string) string {
	var sb strings.Builder
	sb.WriteString("{")
	for _, l := range m.GetLabel() {
		fmt.Fprintf(&sb, "%s=%q,", l.GetName(), l.GetValue())
	}
	fmt.Fprintf(&sb, "%s=%q}", extraName, extraValue)
	return sb.String()
}

func fmtFloat(v float64) string {
	if math.IsInf(v, +1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	if math.IsNaN(v) {
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
