package metrics

import "time"

// WSMetricsAdapter implements nastygo.ClientMetrics using the Prometheus metrics
// registered in this package.
type WSMetricsAdapter struct{}

func (WSMetricsAdapter) SetConnectionStatus(connected bool) { SetWSConnectionStatus(connected) }
func (WSMetricsAdapter) RecordReconnection()                { RecordWSReconnection() }
func (WSMetricsAdapter) RecordMessage(direction string)     { RecordWSMessage(direction) }
func (WSMetricsAdapter) RecordMessageDuration(method string, d time.Duration) {
	RecordWSMessageDuration(method, d)
}
func (WSMetricsAdapter) SetConnectionDuration(d time.Duration) { SetWSConnectionDuration(d) }
