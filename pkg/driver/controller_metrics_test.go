package driver

import (
	"context"
	"strings"
	"testing"

	nastyapi "github.com/nasty-project/nasty-go"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSeedVolumeCapacityMetrics(t *testing.T) {
	subvol := func(name, cluster, protocol, capacity string) nastyapi.Subvolume {
		props := map[string]string{
			nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
			nastyapi.PropertyProtocol:      protocol,
			nastyapi.PropertyCapacityBytes: capacity,
		}
		if cluster != "" {
			props[nastyapi.PropertyClusterID] = cluster
		}
		return nastyapi.Subvolume{Filesystem: "seedfs", Name: name, Properties: props}
	}
	client := &mockAPIClient{
		FindManagedSubvolumesFunc: func(_ context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
			if filesystem != "" {
				t.Fatalf("expected a search across all filesystems, got %q", filesystem)
			}
			return []nastyapi.Subvolume{
				subvol("ours", "cluster-a", "nvmeof", "1073741824"),
				subvol("legacy", "", "nfs", "2147483648"),
				subvol("theirs", "cluster-b", "iscsi", "4294967296"),
				subvol("noproto", "cluster-a", "", "1073741824"),
				subvol("nocap", "cluster-a", "smb", ""),
			}, nil
		},
	}

	s := NewControllerService(client, nil, "cluster-a")
	if err := s.seedVolumeCapacityMetrics(context.Background()); err != nil {
		t.Fatalf("seedVolumeCapacityMetrics: %v", err)
	}

	got := seededCapacities(t)
	want := map[string]float64{
		"seedfs/ours":   1073741824,
		"seedfs/legacy": 2147483648,
	}
	if len(got) != len(want) {
		t.Fatalf("seeded %v, want %v", got, want)
	}
	for id, v := range want {
		if got[id] != v {
			t.Errorf("capacity for %s = %v, want %v", id, got[id], v)
		}
	}
}

// seededCapacities returns this test's volume_capacity_bytes series by volume ID.
func seededCapacities(t *testing.T) map[string]float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "nasty_csi_volume_capacity_bytes" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "volume_id" && strings.HasPrefix(l.GetValue(), "seedfs/") {
					out[l.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	return out
}
