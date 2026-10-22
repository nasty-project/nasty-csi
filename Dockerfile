# Build stage
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

# Version information - passed from CI/CD or Makefile
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace

# Install build dependencies
RUN apk add --no-cache make git

# Use the same pinned SDK in local, CI and container builds.
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the driver for target platform with version info
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    VERSION=${VERSION} GIT_COMMIT=${GIT_COMMIT} BUILD_DATE=${BUILD_DATE} \
    make build

# Final stage - use distroless or minimal base to avoid trigger issues
FROM alpine:3.24

# Install runtime dependencies
# Note: apk exit code 4 means trigger scripts failed but packages are installed (expected under QEMU emulation)
# ca-certificates-bundle ships the CA file itself, so upgrading it doesn't
# depend on the update-ca-certificates trigger running under QEMU.
RUN apk upgrade --no-cache ca-certificates-bundle
RUN apk add --no-cache \
    ca-certificates \
    nfs-utils \
    e2fsprogs \
    e2fsprogs-extra \
    xfsprogs \
    blkid \
    util-linux \
    eudev \
    nvme-cli \
    open-iscsi \
    cifs-utils \
    || [ $? -eq 4 ]

# Copy the driver binary
COPY --from=builder /workspace/bin/nasty-csi-driver /usr/local/bin/

# Set the entrypoint
ENTRYPOINT ["/usr/local/bin/nasty-csi-driver"]
