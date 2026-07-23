#!/usr/bin/env bash

set -euo pipefail

PREBUILT_IMAGE=${1:?prebuilt image flag is required}
K3S_VERSION=${2:?K3S version is required}
SSH_PUBLIC_KEY_FILE=${3:?SSH public key file is required}
OUTPUT_DIR=${4:?output directory is required}

SSH_PUBLIC_KEY=$(< "$SSH_PUBLIC_KEY_FILE")
mkdir -p "$OUTPUT_DIR"

cat > "$OUTPUT_DIR/meta-data" <<'EOF'
instance-id: qemu-e2e-test
local-hostname: qemu-k3s
EOF

cat > "$OUTPUT_DIR/user-data" <<EOF
#cloud-config
users:
  - name: ci
    ssh_authorized_keys:
      - $SSH_PUBLIC_KEY
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
EOF

if [[ "$PREBUILT_IMAGE" == true ]]; then
  cat >> "$OUTPUT_DIR/user-data" <<'EOF'

package_update: false
EOF
else
  cat >> "$OUTPUT_DIR/user-data" <<'EOF'

package_update: true
packages:
  - open-iscsi
  - nfs-common
  - cifs-utils
  - nvme-cli
EOF
fi

cat >> "$OUTPUT_DIR/user-data" <<'EOF'

write_files:
  - path: /etc/modules-load.d/csi.conf
    content: |
      nvme-tcp
      nvme-fabrics
      iscsi_tcp
      nfs
      nfsv4

runcmd:
EOF

if [[ "$PREBUILT_IMAGE" != true ]]; then
  printf "  - curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION='%s' INSTALL_K3S_EXEC='server --write-kubeconfig-mode 644 --disable=traefik --disable=local-storage' INSTALL_K3S_SKIP_ENABLE=true INSTALL_K3S_SKIP_START=true sh -\n" \
    "$K3S_VERSION" >> "$OUTPUT_DIR/user-data"
fi

cat >> "$OUTPUT_DIR/user-data" <<'EOF'
  - test -f /lib/modules/$(uname -r)/kernel/drivers/nvme/host/nvme-tcp.ko || apt-get install -y linux-modules-extra-$(uname -r)
  - depmod -a
  - install -d -m 0755 /etc/nvme /etc/iscsi
  - sh -c 'nvme gen-hostnqn > /etc/nvme/hostnqn'
  - sh -c 'cat /proc/sys/kernel/random/uuid > /etc/nvme/hostid'
  - sh -c 'printf "InitiatorName=%s\n" "$(iscsi-iname)" > /etc/iscsi/initiatorname.iscsi'
  - modprobe nvme-tcp
  - modprobe nvme-fabrics
  - modprobe iscsi_tcp
  - modprobe nfs
  - modprobe nfsv4
  - systemctl enable --now iscsid
  - systemctl enable --now k3s
  - timeout 5m bash -c "until kubectl get nodes --no-headers 2>/dev/null | grep -q ' Ready'; do sleep 2; done"
  - echo "K3S_READY" > /tmp/k3s-ready
EOF
