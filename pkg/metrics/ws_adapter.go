package metrics

import "time"

// WSMetricsAdapter implements nastygo.ClientMetrics using the Prometheus metrics
// registered in this package.
type WSMetricsAdapter struct{}

// SetConnectionStatus updates the WebSocket connection status metric.
func (WSMetricsAdapter) SetConnectionStatus(connected bool) { SetWSConnectionStatus(connected) }

// RecordReconnection records a WebSocket reconnection event.
func (WSMetricsAdapter) RecordReconnection() { RecordWSReconnection() }

// RecordMessage records a WebSocket message.
func (WSMetricsAdapter) RecordMessage(direction string) { RecordWSMessage(direction) }

// RecordMessageDuration records the duration of a WebSocket message.
func (WSMetricsAdapter) RecordMessageDuration(method string, d time.Duration) {
	RecordWSMessageDuration(method, d)
}

// SetConnectionDuration sets the WebSocket connection duration metric.
func (WSMetricsAdapter) SetConnectionDuration(d time.Duration) { SetWSConnectionDuration(d) }
