# Versioning

This document describes the versioning strategy for nasty-csi.

## Version Format

nasty-csi follows [Semantic Versioning](https://semver.org/) (SemVer):

- **MAJOR.MINOR.PATCH** (e.g., `v0.17.3`)
- Tags are prefixed with `v` (e.g., `v0.17.3`, `v1.0.0`)

## Version Sources

The version is determined at **build time** and embedded in the binary. The version comes from:

1. **Git tags** (preferred) - When building from a tagged commit, the version is the tag name
2. **Git describe** - For non-tagged commits, format is `v0.17.3-3-gabc1234` (3 commits after v0.17.3)
3. **"dev"** - Fallback when git is not available

## What's Embedded

Each build includes:

| Field | Description | Example |
|-------|-------------|---------|
| Version | Semantic version from git tag | `v0.17.3` |
| Git Commit | Short SHA of the commit | `abc1234` |
| Build Date | Source commit timestamp | `2025-12-21T10:30:00Z` |
| Go Version | Go compiler version | `go1.26.0` |
| Platform | OS and architecture | `linux/amd64` |

## Checking the Version

### From the Binary

```bash
nasty-csi-driver --version
```

Output:
```
nasty.csi.io version: v0.17.3
  Git commit: abc1234
  Build date: 2025-12-21T10:30:00Z
  Go version: go1.26.0
  Platform:   linux/amd64
```

### From the Metrics Endpoint

The driver exposes version info via HTTP:

```bash
# Port-forward to the controller pod
kubectl port-forward -n kube-system deployment/nasty-csi-controller 8080:8080

# Query version endpoint
curl http://localhost:8080/version
```

Response:
```json
{
  "version": "v0.17.3",
  "gitCommit": "abc1234",
  "buildDate": "2025-12-21T10:30:00Z",
  "goVersion": "go1.26.0",
  "platform": "linux/amd64"
}
```

### From Container Logs

The version is logged at startup:
```
Starting NASty CSI Driver v0.17.3 (commit: abc1234, built: 2025-12-21T10:30:00Z)
```

### From Helm

Check which version is deployed:
```bash
helm list -n kube-system
```

## Docker Image Tags

### Release Tags

When a version is released, Docker images are tagged with:

| Tag | Description | Stability |
|-----|-------------|-----------|
| `v0.17.3` | Exact version | Immutable |
| `v0.17` | Major.Minor | Points to latest patch |
| `v0` | Major only | Points to latest minor |
| `latest` | Most recent release | Mutable - not recommended for production |

### Development Images

The manually triggered CI workflow publishes the mutable `latest` tag. Release
tags are produced only by the guarded release workflow.

## Helm Chart Versioning

The Helm chart and application are versioned independently. A chart release
records the CSI image version it deploys in `appVersion`:

| Chart.yaml Field | Value |
|------------------|-------|
| `version` | Chart version without a `v` prefix |
| `appVersion` | CSI image version with a `v` prefix |

### Image Tag Resolution

The Helm chart resolves the image tag in this order:

1. **Explicit override**: `--set image.tag=v0.17.3`
2. **Chart's appVersion**: Uses the CSI version selected by that chart release

## Best Practices

### For Production

**Always pin a specific version:**

```bash
# Install specific chart version (uses matching image tag automatically)
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.17.3 \
  ...
```

Or explicitly set the image tag:
```bash
helm install nasty-csi ./nasty-chart \
  --set image.tag=v0.17.3 \
  ...
```

### For Development

The mutable `latest` tag from manually triggered CI is suitable for development
and testing:

```bash
helm install nasty-csi ./nasty-chart \
  --set image.tag=latest \
  --set image.pullPolicy=Always \
  ...
```

### Upgrading

Check current version before upgrading:
```bash
helm list -n kube-system
kubectl logs -n kube-system deployment/nasty-csi-controller | head -1
```

Upgrade to a new version:
```bash
helm upgrade nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.17.3 \
  --reuse-values
```

## Reporting Issues

When reporting issues, always include the full version information:

```bash
# Get version from logs
kubectl logs -n kube-system deployment/nasty-csi-controller 2>&1 | head -5

# Or from the API
kubectl exec -n kube-system deployment/nasty-csi-controller -- \
  /usr/local/bin/nasty-csi-driver --version
```

Include in your issue:
- Version (e.g., `v0.17.3`)
- Git commit (e.g., `abc1234`)
- How you installed (Helm version, custom image, etc.)
