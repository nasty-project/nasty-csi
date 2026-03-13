# kubectl nasty-csi Plugin

A kubectl plugin for managing NASty CSI driver volumes from the command line.

## Installation

### Via Krew (Recommended)

[Krew](https://krew.sigs.k8s.io/) is the plugin manager for kubectl.

```bash
# Install krew if you haven't already
# See: https://krew.sigs.k8s.io/docs/user-guide/setup/install/

# Install the plugin
kubectl krew install nasty-csi

# Verify installation
kubectl nasty-csi --version
```

### Manual Installation

Download the appropriate binary from [GitHub Releases](https://github.com/nasty-project/nasty-csi/releases):

```bash
# Linux amd64
curl -LO https://github.com/nasty-project/nasty-csi/releases/download/plugin-v0.1.0/kubectl-nasty-csi-linux-amd64.tar.gz
tar -xzf kubectl-nasty-csi-linux-amd64.tar.gz
mv kubectl-nasty-csi-linux-amd64/kubectl-nasty-csi /usr/local/bin/

# macOS arm64 (Apple Silicon)
curl -LO https://github.com/nasty-project/nasty-csi/releases/download/plugin-v0.1.0/kubectl-nasty-csi-darwin-arm64.tar.gz
tar -xzf kubectl-nasty-csi-darwin-arm64.tar.gz
mv kubectl-nasty-csi-darwin-arm64/kubectl-nasty-csi /usr/local/bin/

# Verify
kubectl nasty-csi --version
```

## Configuration

The plugin automatically discovers NASty credentials from the installed driver, so it **works out of the box** on clusters with nasty-csi installed.

### Credential Discovery Priority

1. **Explicit flags**: `--url` and `--api-key`
2. **Explicit secret**: `--secret namespace/name`
3. **Auto-discovery**: Searches `kube-system` for driver secrets
4. **Environment variables**: `NASTY_URL` and `NASTY_API_KEY`

### Examples

```bash
# On a cluster with nasty-csi installed - just works!
kubectl nasty-csi list

# Explicit credentials via flags
kubectl nasty-csi list --url wss://nasty:443/api/current --api-key YOUR-API-KEY

# Using a specific secret
kubectl nasty-csi list --secret kube-system/my-nasty-secret

# Via environment variables
export NASTY_URL=wss://nasty:443/api/current
export NASTY_API_KEY=YOUR-API-KEY
kubectl nasty-csi list
```

## Commands

### Overview Commands

#### `summary`
Display a dashboard-style overview of all nasty-csi managed resources.

```bash
kubectl nasty-csi summary
```

Output:
```
╔════════════════════════════════════════════════════════════════╗
║                    NASty CSI Summary                             ║
╠════════════════════════════════════════════════════════════════╣
║  VOLUMES                                                       ║
║    Total: 12    NFS: 8    NVMe-oF: 4    Clones: 2             ║
╠────────────────────────────────────────────────────────────────╣
║  SNAPSHOTS                                                     ║
║    Total: 5     Attached: 3    Detached: 2                    ║
╠────────────────────────────────────────────────────────────────╣
║  CAPACITY                                                      ║
║    Provisioned: 500 GiB    Used: 125 GiB    (25.0%)           ║
╠────────────────────────────────────────────────────────────────╣
║  HEALTH                                                        ║
║    ✓ Healthy: 12                                              ║
╚════════════════════════════════════════════════════════════════╝
```

### Listing Commands

#### `list`
List all nasty-csi managed volumes with their properties.

```bash
kubectl nasty-csi list
kubectl nasty-csi list -o json    # JSON output
kubectl nasty-csi list -o yaml    # YAML output
```

Shows: Dataset, Volume ID, Protocol, Capacity, Adoptable status, Clone source

#### `list-snapshots`
List all snapshots (both attached ZFS snapshots and detached snapshot datasets).

```bash
kubectl nasty-csi list-snapshots
```

Shows: Snapshot name, Source volume, Protocol, Type (attached/detached)

#### `list-orphaned`
Find volumes that exist on NASty but have no matching PVC in Kubernetes.

```bash
kubectl nasty-csi list-orphaned
```

Useful for disaster recovery and cleanup scenarios.

#### `list-clones`
List all cloned volumes with their dependency relationships.

```bash
kubectl nasty-csi list-clones
kubectl nasty-csi list-clones -o yaml
```

Shows clone mode and helps understand what can and cannot be deleted:
- **cow** (Copy-on-Write): Clone depends on snapshot. Snapshot CANNOT be deleted.
- **promoted**: Snapshot depends on clone. Snapshot CAN be deleted.
- **detached**: No dependency. Both can be deleted independently.

#### `list-unmanaged`
List volumes not managed by nasty-csi (useful for discovering volumes to import).

```bash
kubectl nasty-csi list-unmanaged --pool storage
kubectl nasty-csi list-unmanaged --parent storage/k8s
kubectl nasty-csi list-unmanaged --pool storage --all    # Include system datasets
kubectl nasty-csi list-unmanaged --pool storage -o json
```

Shows:
- Dataset path and name
- Type (filesystem or zvol)
- Detected protocol (NFS if share exists)
- Size information
- Any existing management markers (e.g., democratic-csi)

| Flag | Description |
|------|-------------|
| `--pool` | ZFS pool to search in |
| `--parent` | Parent dataset path to search under |
| `--all` | Show all datasets including system datasets |

### Diagnostic Commands

#### `describe`
Show detailed information about a specific volume.

```bash
kubectl nasty-csi describe <volume-id>
kubectl nasty-csi describe tank/csi/pvc-xxx    # By dataset path
```

Shows: Volume details, capacity, NFS share or NVMe subsystem info, all ZFS properties

#### `health`
Check the health of all managed volumes.

```bash
kubectl nasty-csi health           # Show only issues
kubectl nasty-csi health --all     # Show all volumes
```

Checks:
- Dataset exists on NASty
- NFS shares are present and enabled
- NVMe-oF subsystems are present and enabled

#### `troubleshoot`
Comprehensive diagnostics for a PVC that isn't working.

```bash
kubectl nasty-csi troubleshoot <pvc-name> -n <namespace>
kubectl nasty-csi troubleshoot my-pvc -n default --logs
```

Checks:
- PVC exists and is bound
- PV exists and has valid handle
- NASty connection works
- Dataset exists
- NFS share / NVMe subsystem is healthy
- Recent events and controller logs

#### `connectivity`
Test connection to NASty.

```bash
kubectl nasty-csi connectivity
```

### Maintenance Commands

#### `cleanup`
Delete orphaned volumes from NASty.

```bash
kubectl nasty-csi cleanup                    # Dry-run (preview only)
kubectl nasty-csi cleanup --execute          # Actually delete (with confirmation)
kubectl nasty-csi cleanup --execute --yes    # Delete without confirmation
kubectl nasty-csi cleanup --execute --force  # Delete even non-adoptable volumes
```

Safety features:
- Dry-run by default
- Requires confirmation before deletion
- Only deletes volumes marked as adoptable (unless `--force`)
- Properly cleans up NFS shares and NVMe subsystems

#### `mark-adoptable`
Mark volumes as adoptable for disaster recovery or migration.

```bash
kubectl nasty-csi mark-adoptable <volume-id>           # Mark single volume
kubectl nasty-csi mark-adoptable --all                 # Mark all volumes
kubectl nasty-csi mark-adoptable --unmark <volume-id>  # Remove flag
kubectl nasty-csi mark-adoptable --unmark --all        # Remove from all
```

### Adoption Commands

**For complete adoption workflows including Kubernetes-side steps, see [ADOPTION.md](ADOPTION.md).**

The commands below handle the NASty-side operations. Full adoption also requires Kubernetes-side steps (scaling down workloads, managing PVCs, etc.) which are documented in the adoption guide.

#### `import`
Import an existing dataset into nasty-csi management.

```bash
# Import an NFS dataset (auto-detect existing share)
kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs

# Import and create NFS share if missing
kubectl nasty-csi import storage/data/myvolume --protocol nfs --create-share

# Import with custom volume ID
kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs --volume-id my-volume

# Dry run to see what would happen
kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs --dry-run
```

Useful for:
- Migrating volumes from democratic-csi
- Adopting manually created datasets
- Taking over volumes from other CSI drivers

| Flag | Description |
|------|-------------|
| `--protocol` | Protocol: nfs or nvmeof (required) |
| `--volume-id` | Custom volume ID (defaults to dataset name) |
| `--create-share` | Create NFS share if it doesn't exist |
| `--storage-class` | StorageClass to associate with the volume |
| `--dry-run` | Show what would be done without making changes |

After importing, use `kubectl nasty-csi adopt <dataset>` to generate PV/PVC manifests.

#### `adopt`
Generate a PersistentVolume manifest to adopt an existing volume.

```bash
kubectl nasty-csi adopt <dataset-path>
kubectl nasty-csi adopt tank/csi/my-volume -o yaml > pv.yaml
kubectl apply -f pv.yaml
```

#### `status`
Show the current status of a volume from NASty.

```bash
kubectl nasty-csi status <pvc-name>
```

### Web Dashboard

#### `serve`
Start a web-based dashboard for viewing nasty-csi resources in your browser.

```bash
# Start dashboard on default port 2137
kubectl nasty-csi serve

# Start on custom port
kubectl nasty-csi serve --port 9090

# With pool for unmanaged volume discovery
kubectl nasty-csi serve --pool storage
```

The dashboard provides:
- **Summary cards** - Total volumes, snapshots, clones, and capacity
- **Volumes tab** - All managed volumes with protocol, capacity, and adoptable status
- **Snapshots tab** - All snapshots with source volume and type (attached/detached)
- **Clones tab** - Cloned volumes with dependency information
- **Unmanaged tab** - Volumes not managed by nasty-csi (requires `--pool` flag)

Features:
- Dark theme UI
- Real-time refresh via htmx
- Auto-detects democratic-csi managed volumes
- Shows container datasets vs actual volumes

| Flag | Description |
|------|-------------|
| `--port` | Port to listen on (default: 2137) |
| `--pool` | ZFS pool to search for unmanaged volumes |

Access the dashboard at `http://localhost:2137` after starting.

## Output Formats

All commands support multiple output formats:

```bash
kubectl nasty-csi list              # Table (default)
kubectl nasty-csi list -o table     # Table (explicit)
kubectl nasty-csi list -o json      # JSON
kubectl nasty-csi list -o yaml      # YAML
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--url` | NASty WebSocket URL (wss://host/api/current) |
| `--api-key` | NASty API key |
| `--secret` | Kubernetes secret with credentials (namespace/name) |
| `-o, --output` | Output format: table, json, yaml |
| `--insecure-skip-tls-verify` | Skip TLS verification (default: true) |

## Use Cases

### Disaster Recovery

1. Prepare volumes for potential cluster loss:
   ```bash
   kubectl nasty-csi mark-adoptable --all
   ```

2. After cluster recreation, find orphaned volumes:
   ```bash
   kubectl nasty-csi list-orphaned
   ```

3. Adopt volumes into the new cluster:
   ```bash
   kubectl nasty-csi adopt tank/csi/pvc-xxx > pv.yaml
   kubectl apply -f pv.yaml
   ```

### Routine Maintenance

1. Check overall health:
   ```bash
   kubectl nasty-csi summary
   kubectl nasty-csi health
   ```

2. Clean up orphaned volumes:
   ```bash
   kubectl nasty-csi cleanup              # Preview
   kubectl nasty-csi cleanup --execute    # Clean up
   ```

### Troubleshooting

1. PVC stuck in Pending:
   ```bash
   kubectl nasty-csi troubleshoot my-pvc -n default --logs
   ```

2. Check specific volume:
   ```bash
   kubectl nasty-csi describe pvc-xxx
   ```

## Building from Source

```bash
# Clone the repository
git clone https://github.com/nasty-project/nasty-csi.git
cd nasty-csi

# Build the plugin
go build -o kubectl-nasty-csi ./cmd/kubectl-nasty-csi

# Install
mv kubectl-nasty-csi /usr/local/bin/
```
