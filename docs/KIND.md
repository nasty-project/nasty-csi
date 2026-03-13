# Testing with Kind (Kubernetes in Docker)

This guide shows how to test the NASty CSI driver in a local Kind cluster with NFS support.

> **Note:** This guide is for **local development only**. The project's CI/CD pipeline uses k3s on self-hosted runners for integration testing against real NASty infrastructure. Kind is suitable for NFS development/testing but has limitations for NVMe-oF testing.

## Prerequisites

1. **Docker**: Running and accessible
2. **Kind**: Install from https://kind.sigs.k8s.io/docs/user/quick-start/
3. **kubectl**: Kubernetes CLI tool
4. **NASty**: Accessible NASty server with API access

## Quick Start with Helm (Recommended)

### 1. Create Kind Cluster

```bash
kind create cluster --config kind-config.yaml --name nasty-csi-test
```

### 2. Install NFS Support

Install `nfs-common` package on all Kind nodes, which is required for NFS mounts:

```bash
# For each Kind node (control-plane and workers)
docker exec nasty-csi-test-control-plane apt-get update
docker exec nasty-csi-test-control-plane apt-get install -y nfs-common

# If you have worker nodes
docker exec nasty-csi-test-worker apt-get update
docker exec nasty-csi-test-worker apt-get install -y nfs-common
```

### 3. Install CSI Driver via Helm

```bash
# Install from OCI registry
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --version 0.17.3 \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="nasty-csi-nfs" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="nfs" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-NASTY-IP"

# Verify deployment
kubectl get pods -n kube-system -l app.kubernetes.io/name=nasty-csi-driver
```

### 4. Test the Driver

Create a test PVC and pod:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: nasty-nfs
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'Hello from Kind!' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF

# Check status
kubectl get pvc test-pvc
kubectl get pod test-pod
kubectl exec test-pod -- cat /data/test.txt
```

---

## Manual Setup (Step-by-Step)

<details>
<summary>Manual deployment for development/testing - Click to expand</summary>

If you prefer to set up manually or understand each step:

### 1. Create Kind Cluster

```bash
kind create cluster --config kind-config.yaml
```

### 2. Install NFS Support

Install `nfs-common` package on all Kind nodes:

```bash
# For each Kind node (control-plane and workers)
docker exec nasty-csi-test-control-plane apt-get update
docker exec nasty-csi-test-control-plane apt-get install -y nfs-common

# If you have worker nodes
docker exec nasty-csi-test-worker apt-get update
docker exec nasty-csi-test-worker apt-get install -y nfs-common
```

### 3. Build and Load Image

```bash
# Build
docker build -t bfenski/nasty-csi:v0.17.3 .

# Load into Kind
kind load docker-image bfenski/nasty-csi:v0.17.3 --name nasty-csi-test
```

### 4. Create Kubernetes Secret

```bash
# Load credentials
source .tns-credentials

# Create secret
kubectl create secret generic nasty-csi-secret \
  --namespace=kube-system \
  --from-literal=url="$NASTY_URL" \
  --from-literal=api-key="$NASTY_API_KEY"
```

### 5. Deploy CSI Driver

```bash
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/csidriver.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
kubectl apply -f deploy/storageclass.yaml
```

### 6. Verify Deployment

```bash
# Check pods
kubectl get pods -n kube-system -l 'app in (nasty-csi-controller,nasty-csi-node)'

# Check logs
kubectl logs -n kube-system -l app=nasty-csi-controller -c nasty-csi-plugin
kubectl logs -n kube-system -l app=nasty-csi-node -c nasty-csi-plugin
```

</details>

---

## Troubleshooting

### NFS Mount Issues

If pods fail to mount NFS volumes:

1. **Check NFS client installation:**
   ```bash
   docker exec nasty-csi-test-control-plane which mount.nfs
   docker exec nasty-csi-test-worker which mount.nfs
   ```

2. **Verify network connectivity:**
   ```bash
   # From a pod in the cluster
   kubectl run -it --rm debug --image=alpine --restart=Never -- sh
   apk add nfs-utils
   showmount -e YOUR-NASTY-IP  # Replace with your NASty IP
   ```

3. **Check NASty NFS service:**
   - Ensure NFS service is running in NASty
   - Verify NFS shares exist
   - Check firewall allows NFS (port 2049)

### Pod Stuck in ContainerCreating

Check node plugin logs:

For Helm deployments:
```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=node -c nasty-csi-plugin --tail=100
```

For manual/script deployments:
```bash
kubectl logs -n kube-system -l app=nasty-csi-node -c nasty-csi-plugin --tail=100
```

Common issues:
- NFS client not installed (install nfs-common in Kind nodes)
- Cannot reach NASty server (check network/firewall)
- Invalid NFS export path

### PVC Stuck in Pending

Check controller logs:

For Helm deployments:
```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=controller -c nasty-csi-plugin --tail=100
```

For manual/script deployments:
```bash
kubectl logs -n kube-system -l app=nasty-csi-controller -c nasty-csi-plugin --tail=100
```

Common issues:
- Invalid NASty credentials
- Pool doesn't exist
- Network connectivity issues

### Debug with Verbose Logging

Edit the deployment files to increase log verbosity:

```yaml
# In deploy/controller.yaml and deploy/node.yaml
args:
  - "--v=10"  # Increase from 5 to 10
```

Then restart:

For Helm deployments:
```bash
kubectl rollout restart statefulset -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=controller
kubectl rollout restart daemonset -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=node
```

For manual/script deployments:
```bash
kubectl rollout restart statefulset -n kube-system nasty-csi-controller
kubectl rollout restart daemonset -n kube-system nasty-csi-node
```

## Testing Scenarios

### Test 1: Basic Volume Creation

```bash
kubectl apply -f test-pvc.yaml
kubectl get pvc test-pvc-nfs -w
```

### Test 2: Data Persistence

```bash
# Write data
kubectl exec test-nfs-pod -- sh -c "echo 'persistent data' > /data/persistent.txt"

# Delete pod
kubectl delete pod test-nfs-pod

# Recreate pod
kubectl apply -f test-pvc.yaml

# Verify data
kubectl exec test-nfs-pod -- cat /data/persistent.txt
```

### Test 3: Multiple Volumes

```bash
for i in {1..3}; do
  kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-$i
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: tns-nfs
  resources:
    requests:
      storage: 500Mi
EOF
done

kubectl get pvc
```

## Cleanup

### Delete test resources:

```bash
kubectl delete pvc test-pvc
kubectl delete pod test-pod
```

### Uninstall CSI driver:

For Helm installations:
```bash
helm uninstall nasty-csi -n kube-system
```

For manual/script deployments:
```bash
kubectl delete -f deploy/storageclass.yaml
kubectl delete -f deploy/node.yaml
kubectl delete -f deploy/controller.yaml
kubectl delete -f deploy/csidriver.yaml
kubectl delete -f deploy/rbac.yaml
kubectl delete secret nasty-csi-secret -n kube-system
```

### Delete Kind cluster:

```bash
kind delete cluster --name nasty-csi-test
```

## Network Considerations for Kind

### Accessing NASty from Kind

Kind containers run in Docker's network. Ensure Kind can reach your NASty server:

1. **If NASty is on your local network:**
   - Use the actual IP address (e.g., YOUR-NASTY-IP)
   - Docker should be able to route to it

2. **If NASty is on localhost:**
   - Use host.docker.internal instead of 127.0.0.1
   - Or use the host's network IP

3. **Test connectivity from Kind:**
   ```bash
   kubectl run -it --rm test --image=alpine --restart=Never -- sh
   # Inside the pod:
   ping -c 3 YOUR-NASTY-IP
   nc -zv YOUR-NASTY-IP 2049  # Test NFS port
   ```

## Performance Notes

Kind is suitable for:
- Development and testing
- CI/CD pipelines
- Feature validation
- Bug reproduction

Kind is NOT suitable for:
- Production workloads
- Performance benchmarking
- Load testing

For production testing, use a real Kubernetes cluster (EKS, GKE, AKS, or on-premises).

## Next Steps

After successful Kind testing:

1. Test on a real Kubernetes cluster
2. Implement volume snapshots
3. Add volume expansion support
4. Test with StatefulSets
5. Load testing with multiple concurrent volumes
