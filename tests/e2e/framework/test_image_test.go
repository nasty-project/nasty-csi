package framework

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

func TestWorkloadImageIsPinned(t *testing.T) {
	if !regexp.MustCompile(`^docker\.io/library/busybox@sha256:[a-f0-9]{64}$`).MatchString(TestImage()) {
		t.Fatalf("workload image must be pinned: %q", TestImage())
	}
}

func TestStatefulSetUsesCachedPinnedImage(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\ncat > \"$FIXTURE_OUTPUT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FIXTURE_OUTPUT", output)
	client := &KubernetesClient{namespace: "fixture"}
	if err := client.CreateStatefulSet(t.Context(), StatefulSetOptions{Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var statefulSet appsv1.StatefulSet
	if err := yaml.Unmarshal(data, &statefulSet); err != nil {
		t.Fatal(err)
	}
	container := statefulSet.Spec.Template.Spec.Containers[0]
	if container.Image != TestImage() || container.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("StatefulSet bypasses preloaded image: %+v", container)
	}
}

func TestCreatePodUsesCachedPinnedImage(t *testing.T) {
	var created corev1.Pod
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Errorf("decode pod: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&created); err != nil {
			t.Errorf("encode pod: %v", err)
		}
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	client := &KubernetesClient{clientset: clientset, namespace: "fixture"}
	for _, mode := range []corev1.PersistentVolumeMode{corev1.PersistentVolumeFilesystem, corev1.PersistentVolumeBlock} {
		pod, err := client.CreatePod(t.Context(), PodOptions{Name: "fixture", PVCName: "fixture", MountPath: "/data", VolumeMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		container := pod.Spec.Containers[0]
		if container.Image != TestImage() || container.ImagePullPolicy != corev1.PullIfNotPresent {
			t.Fatalf("pod bypasses preloaded image: %+v", container)
		}
	}
}
