package main

import (
	"testing"
)

func TestParsePrometheusMetrics(t *testing.T) {
	tests := []struct {
		check func(*testing.T, *MetricsSummary)
		name  string
		data  string
	}{
		{
			name: "empty string yields zeroed summary",
			data: "",
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.WebSocketConnected {
					t.Error("WebSocketConnected should be false for empty input")
				}
				if s.TotalOperations != 0 {
					t.Errorf("TotalOperations = %d, want 0", s.TotalOperations)
				}
				if s.MessagesSent != 0 {
					t.Errorf("MessagesSent = %d, want 0", s.MessagesSent)
				}
				if s.MessagesReceived != 0 {
					t.Errorf("MessagesReceived = %d, want 0", s.MessagesReceived)
				}
			},
		},
		{
			name: "websocket connected metric",
			data: `tns_csi_websocket_connection_status 1`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if !s.WebSocketConnected {
					t.Error("WebSocketConnected should be true when metric value is 1")
				}
			},
		},
		{
			name: "websocket disconnected metric",
			data: `tns_csi_websocket_connection_status 0`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.WebSocketConnected {
					t.Error("WebSocketConnected should be false when metric value is 0")
				}
			},
		},
		{
			name: "websocket reconnection counter",
			data: `tns_csi_websocket_reconnections_total 5`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.WebSocketReconnects != 5 {
					t.Errorf("WebSocketReconnects = %d, want 5", s.WebSocketReconnects)
				}
			},
		},
		{
			name: "message counters",
			data: `tns_csi_websocket_messages_total{direction="sent"} 42
tns_csi_websocket_messages_total{direction="received"} 38`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.MessagesSent != 42 {
					t.Errorf("MessagesSent = %d, want 42", s.MessagesSent)
				}
				if s.MessagesReceived != 38 {
					t.Errorf("MessagesReceived = %d, want 38", s.MessagesReceived)
				}
			},
		},
		{
			name: "volume operations with labels",
			data: `tns_csi_volume_operations_total{protocol="nfs",operation="create",status="success"} 10
tns_csi_volume_operations_total{protocol="nfs",operation="delete",status="success"} 3
tns_csi_volume_operations_total{protocol="nvmeof",operation="create",status="success"} 7
tns_csi_volume_operations_total{protocol="nvmeof",operation="create",status="error"} 2
tns_csi_volume_operations_total{protocol="iscsi",operation="expand",status="success"} 1`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.NFSOperations != 13 {
					t.Errorf("NFSOperations = %d, want 13", s.NFSOperations)
				}
				if s.NVMeOFOperations != 9 {
					t.Errorf("NVMeOFOperations = %d, want 9", s.NVMeOFOperations)
				}
				if s.ISCSIOperations != 1 {
					t.Errorf("ISCSIOperations = %d, want 1", s.ISCSIOperations)
				}
				if s.CreateOperations != 19 {
					t.Errorf("CreateOperations = %d, want 19", s.CreateOperations)
				}
				if s.DeleteOperations != 3 {
					t.Errorf("DeleteOperations = %d, want 3", s.DeleteOperations)
				}
				if s.ExpandOperations != 1 {
					t.Errorf("ExpandOperations = %d, want 1", s.ExpandOperations)
				}
				if s.SuccessOperations != 21 {
					t.Errorf("SuccessOperations = %d, want 21", s.SuccessOperations)
				}
				if s.ErrorOperations != 2 {
					t.Errorf("ErrorOperations = %d, want 2", s.ErrorOperations)
				}
				if s.TotalOperations != 23 {
					t.Errorf("TotalOperations = %d, want 23 (success + error)", s.TotalOperations)
				}
			},
		},
		{
			name: "comments and blank lines are skipped",
			data: `# HELP tns_csi_websocket_connection_status WebSocket connection status
# TYPE tns_csi_websocket_connection_status gauge

tns_csi_websocket_connection_status 1

# HELP tns_csi_websocket_reconnections_total Total reconnections
# TYPE tns_csi_websocket_reconnections_total counter
tns_csi_websocket_reconnections_total 3`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if !s.WebSocketConnected {
					t.Error("WebSocketConnected should be true")
				}
				if s.WebSocketReconnects != 3 {
					t.Errorf("WebSocketReconnects = %d, want 3", s.WebSocketReconnects)
				}
			},
		},
		{
			name: "mixed metrics with all fields",
			data: `# HELP tns_csi_websocket_connection_status WebSocket connection status
tns_csi_websocket_connection_status 1
tns_csi_websocket_reconnections_total 2
tns_csi_websocket_connection_duration_seconds 3600.5
tns_csi_websocket_messages_total{direction="sent"} 100
tns_csi_websocket_messages_total{direction="received"} 95
tns_csi_volume_operations_total{protocol="nfs",operation="create",status="success"} 5
tns_csi_volume_operations_total{protocol="nfs",operation="delete",status="error"} 1`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if !s.WebSocketConnected {
					t.Error("WebSocketConnected should be true")
				}
				if s.WebSocketReconnects != 2 {
					t.Errorf("WebSocketReconnects = %d, want 2", s.WebSocketReconnects)
				}
				if s.ConnectionDurationSecs != 3600.5 {
					t.Errorf("ConnectionDurationSecs = %f, want 3600.5", s.ConnectionDurationSecs)
				}
				if s.MessagesSent != 100 {
					t.Errorf("MessagesSent = %d, want 100", s.MessagesSent)
				}
				if s.MessagesReceived != 95 {
					t.Errorf("MessagesReceived = %d, want 95", s.MessagesReceived)
				}
				if s.SuccessOperations != 5 {
					t.Errorf("SuccessOperations = %d, want 5", s.SuccessOperations)
				}
				if s.ErrorOperations != 1 {
					t.Errorf("ErrorOperations = %d, want 1", s.ErrorOperations)
				}
				if s.TotalOperations != 6 {
					t.Errorf("TotalOperations = %d, want 6", s.TotalOperations)
				}
				if s.NFSOperations != 6 {
					t.Errorf("NFSOperations = %d, want 6", s.NFSOperations)
				}
				if s.CreateOperations != 5 {
					t.Errorf("CreateOperations = %d, want 5", s.CreateOperations)
				}
				if s.DeleteOperations != 1 {
					t.Errorf("DeleteOperations = %d, want 1", s.DeleteOperations)
				}
			},
		},
		{
			name: "total operations equals success plus error",
			data: `tns_csi_volume_operations_total{protocol="nfs",operation="create",status="success"} 100
tns_csi_volume_operations_total{protocol="nfs",operation="create",status="error"} 25`,
			check: func(t *testing.T, s *MetricsSummary) {
				t.Helper()
				if s.TotalOperations != s.SuccessOperations+s.ErrorOperations {
					t.Errorf("TotalOperations (%d) != SuccessOperations (%d) + ErrorOperations (%d)",
						s.TotalOperations, s.SuccessOperations, s.ErrorOperations)
				}
				if s.TotalOperations != 125 {
					t.Errorf("TotalOperations = %d, want 125", s.TotalOperations)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := parsePrometheusMetrics(tt.data)
			if summary == nil {
				t.Fatal("parsePrometheusMetrics returned nil")
			}
			tt.check(t, summary)
		})
	}
}
