package driver

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type blockedHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (s *blockedHealthServer) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	close(s.entered)
	<-s.release // Simulate a helper ignoring RPC cancellation.
	close(s.finished)
	return &grpc_health_v1.HealthCheckResponse{}, nil
}

func TestShutdownForcesHungRPC(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	health := &blockedHealthServer{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	grpc_health_v1.RegisterHealthServer(server, health)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	t.Cleanup(func() { close(health.release); server.Stop(); _ = listener.Close(); <-serveDone })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		_, _ = grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	}()
	select {
	case <-health.entered:
	case <-ctx.Done():
		t.Fatal("RPC did not start")
	}
	stopped := make(chan struct{})
	go func() { stopGRPCServer(server, 50*time.Millisecond); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on hung RPC")
	}
	select {
	case <-health.finished:
		t.Fatal("test RPC unexpectedly completed")
	default:
	}
	select {
	case <-rpcDone:
	case <-time.After(time.Second):
		t.Fatal("client was not disconnected")
	}
}

func TestShutdownDrainsIdleServer(t *testing.T) {
	server := grpc.NewServer()
	start := time.Now()
	stopGRPCServer(server, time.Second)
	if time.Since(start) >= time.Second {
		t.Fatal("idle server waited for forced-stop deadline")
	}
}
