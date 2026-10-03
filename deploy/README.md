# Deployment

## Standard Installation (Helm)

Helm is the recommended way to install nasty-csi. The raw Kubernetes manifests that were previously in this directory have been removed in favor of the Helm chart.

### Quick Start

```bash
# Install the separately released Helm chart from GHCR
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.10 \
  --namespace kube-system \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

Chart `0.0.10` defaults to driver `v0.0.10`. To deploy driver `v0.0.11`,
add `--set image.tag=v0.0.11`; chart and driver versions are independent.

### Version Pinning

**Always use a specific version in production.** The `--version` flag ensures you get a known, tested release.

See [chart releases](https://github.com/nasty-project/nasty-chart/releases)
for available chart versions and
[driver releases](https://github.com/nasty-project/nasty-csi/releases)
for available image versions.

### Configuration

See the [Helm chart documentation](https://github.com/nasty-project/nasty-chart#readme) for full configuration options.

Common configuration:

```bash
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.10 \
  --namespace kube-system \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set nasty.skipTLSVerify=true \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].pool="tank" \
  --set storageClasses[0].server="YOUR-NASTY-IP" \
  --set storageClasses[1].name=nasty-csi-nvmeof \
  --set storageClasses[1].enabled=true \
  --set storageClasses[1].protocol=nvmeof \
  --set storageClasses[1].pool="tank" \
  --set storageClasses[1].server="YOUR-NASTY-IP"
```

### Upgrading

Upgrade the NASty appliance first. For CSI releases that initialize block filesystems on the backend, suspend the old controller while rolling node plugins:

```bash
kubectl --namespace kube-system scale deployment \
  --selector app.kubernetes.io/instance=nasty-csi \
  --replicas=0

helm upgrade nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.10 \
  --namespace kube-system \
  --reuse-values \
  --set controller.replicas=0

kubectl --namespace kube-system rollout status daemonset \
  --selector app.kubernetes.io/instance=nasty-csi

helm upgrade nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.10 \
  --namespace kube-system \
  --reuse-values \
  --set controller.replicas=1
```

### Uninstalling

```bash
helm uninstall nasty-csi --namespace kube-system
```

## Why Helm?

The Helm chart provides:

1. **Version management** - Pin specific versions for reproducible deployments
2. **Configuration validation** - Fails fast on missing required values
3. **Sensible defaults** - Works out of the box with minimal configuration
4. **Easy upgrades** - `helm upgrade` handles rolling updates
5. **Templating** - Consistent naming and labeling across all resources
