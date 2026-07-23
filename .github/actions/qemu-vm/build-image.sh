#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=/dev/null
source "$SCRIPT_DIR/image.env"

BASE_IMAGE=${BASE_IMAGE:-/tmp/ubuntu-cloud.img}
BUILD_DISK=/tmp/vm-disk.qcow2
OUTPUT_IMAGE=${OUTPUT_IMAGE:-/tmp/ubuntu-prebuilt.qcow2}
SSH_KEY=/tmp/vm-build-key
SSH_PORT=2222
SSH_OPTS=(
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout=10
  -i "$SSH_KEY"
  -p "$SSH_PORT"
)

cleanup() {
  if [[ -f /tmp/vm-build.pid ]]; then
    local pid
    pid=$(< /tmp/vm-build.pid)
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  fi
}

show_guest_diagnostics() {
  echo "::group::VM console"
  tail -n 400 /tmp/vm-build-console.log 2>/dev/null || true
  echo "::endgroup::"
  echo "::group::cloud-init output"
  ssh "${SSH_OPTS[@]}" ci@localhost \
    "sudo tail -n 400 /var/log/cloud-init-output.log; sudo cloud-init status --long" 2>/dev/null || true
  echo "::endgroup::"
}

trap cleanup EXIT

test -s "$BASE_IMAGE"
rm -f "$BUILD_DISK" "$OUTPUT_IMAGE" "$SSH_KEY" "$SSH_KEY.pub" \
  /tmp/vm-build.pid /tmp/vm-build-console.log /tmp/vm-build-cloud-init.iso
rm -rf /tmp/vm-build-cloud-init

cp "$BASE_IMAGE" "$BUILD_DISK"
qemu-img resize "$BUILD_DISK" 20G
ssh-keygen -t ed25519 -f "$SSH_KEY" -N "" -q
VM_SSH_PUB=$(< "$SSH_KEY.pub")

mkdir -p /tmp/vm-build-cloud-init
cat > /tmp/vm-build-cloud-init/meta-data <<'EOF'
instance-id: nasty-csi-qemu-image-builder
local-hostname: nasty-csi-image-builder
EOF

cat > /tmp/vm-build-cloud-init/user-data <<EOF
#cloud-config
users:
  - name: ci
    ssh_authorized_keys:
      - $VM_SSH_PUB
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash

package_update: true
packages:
  - cifs-utils
  - nfs-common
  - nvme-cli
  - open-iscsi

runcmd:
  - apt-get install -y linux-modules-extra-\$(uname -r)
  - curl -fsSL https://tailscale.com/install.sh | sh
  - curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION='$K3S_VERSION' INSTALL_K3S_EXEC='server --write-kubeconfig-mode 644 --disable=traefik --disable=local-storage' INSTALL_K3S_SKIP_ENABLE=true INSTALL_K3S_SKIP_START=true sh -
  - systemctl disable --now tailscaled || true
  - test -x /usr/local/bin/k3s
  - test -x /usr/bin/tailscale
  - touch /tmp/image-build-ready
EOF

genisoimage -output /tmp/vm-build-cloud-init.iso \
  -volid cidata -joliet -rock \
  /tmp/vm-build-cloud-init/user-data /tmp/vm-build-cloud-init/meta-data

qemu-system-x86_64 \
  -enable-kvm \
  -m 4096 \
  -smp 4 \
  -cpu host \
  -drive "file=$BUILD_DISK,format=qcow2,if=virtio" \
  -cdrom /tmp/vm-build-cloud-init.iso \
  -nic "user,model=virtio-net-pci,hostfwd=tcp::$SSH_PORT-:22" \
  -display none \
  -serial file:/tmp/vm-build-console.log \
  -daemonize \
  -pidfile /tmp/vm-build.pid

echo "Waiting for builder VM SSH..."
ssh_ready=false
for attempt in $(seq 1 60); do
  if ssh "${SSH_OPTS[@]}" ci@localhost true 2>/dev/null; then
    echo "SSH ready (attempt $attempt)"
    ssh_ready=true
    break
  fi
  sleep 5
done
if [[ "$ssh_ready" != true ]]; then
  show_guest_diagnostics
  exit 1
fi

echo "Waiting for image provisioning..."
if ! timeout 20m ssh "${SSH_OPTS[@]}" ci@localhost "sudo cloud-init status --wait"; then
  show_guest_diagnostics
  exit 1
fi

ssh "${SSH_OPTS[@]}" ci@localhost \
  "test -f /tmp/image-build-ready && k3s --version && tailscale version && nvme version && iscsiadm --version"

echo "Removing per-instance identity and runtime state..."
ssh "${SSH_OPTS[@]}" ci@localhost 'sudo bash -s' <<'GUEST_CLEANUP'
set -euo pipefail
systemctl disable --now k3s tailscaled 2>/dev/null || true
rm -rf /var/lib/rancher/k3s /var/lib/tailscale
rm -f /etc/iscsi/initiatorname.iscsi /etc/nvme/hostid /etc/nvme/hostnqn
rm -f /etc/ssh/ssh_host_* /root/.bash_history /home/ci/.bash_history
cloud-init clean --logs --seed --machine-id
rm -rf /home/ci/.ssh
sync
systemd-run --on-active=2s --unit=qemu-image-poweroff /usr/bin/systemctl poweroff
GUEST_CLEANUP

echo "Waiting for builder VM shutdown..."
vm_stopped=false
for _ in $(seq 1 60); do
  if ! kill -0 "$(< /tmp/vm-build.pid)" 2>/dev/null; then
    vm_stopped=true
    break
  fi
  sleep 2
done
if [[ "$vm_stopped" != true ]]; then
  echo "::error::Builder VM did not shut down cleanly"
  exit 1
fi

qemu-img check "$BUILD_DISK"
qemu-img convert -p -O qcow2 -c "$BUILD_DISK" "$OUTPUT_IMAGE"
qemu-img check "$OUTPUT_IMAGE"
ls -lh "$OUTPUT_IMAGE"
echo "Built generalized image $IMAGE_REVISION with K3S $K3S_VERSION"
