# Deployment

## Standard Installation (Helm)

Helm is the recommended way to install nasty-csi. The raw Kubernetes manifests that were previously in this directory have been removed in favor of the Helm chart.

### Quick Start

```bash
# Add the OCI registry (Docker Hub)
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP"
```

Or using GitHub Container Registry:

```bash
helm install nasty-csi oci://ghcr.io/fenio/charts/nasty-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP"
```

### Version Pinning

**Always use a specific version in production.** The `--version` flag ensures you get a known, tested release.

To see available versions:
```bash
# Docker Hub
helm search repo oci://registry-1.docker.io/bfenski/nasty-csi-driver --versions

# Or check GitHub releases
# https://github.com/nasty-project/nasty-csi/releases
```

### Configuration

See the [Helm chart documentation](../charts/nasty-csi-driver/README.md) for full configuration options.

Common configuration:

```bash
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set truenas.skipTLSVerify=true \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].pool="tank" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP" \
  --set storageClasses[1].name=nasty-csi-nvmeof \
  --set storageClasses[1].enabled=true \
  --set storageClasses[1].protocol=nvmeof \
  --set storageClasses[1].pool="tank" \
  --set storageClasses[1].server="YOUR-TRUENAS-IP"
```

### Upgrading

```bash
helm upgrade nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --reuse-values
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
