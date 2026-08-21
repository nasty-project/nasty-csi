# Release Process

This document describes how to create a new release of the NASty CSI Driver.

## Prerequisites

Before creating your first release, you need to configure GitHub secrets for Docker Hub authentication.

### Required GitHub Secrets

Navigate to your repository settings: `Settings` → `Secrets and variables` → `Actions` → `New repository secret`

Add the following secrets:

1. **DOCKERHUB_USERNAME**
   - Your Docker Hub username
   - Example: `bfenski`

2. **DOCKERHUB_TOKEN**
   - Docker Hub access token (NOT your password)
   - Generate at: https://hub.docker.com/settings/security
   - Click "New Access Token"
   - Give it a descriptive name like "GitHub Actions - NASty CSI"
   - Select "Read, Write, Delete" permissions
   - Copy the token (you won't see it again)

### Verify Secrets

After adding secrets, they should appear in:
- Repository Settings → Secrets and variables → Actions → Repository secrets

## Release Workflow

The release process is fully automated via GitHub Actions.

### Creating a Release

1. **Ensure main branch is ready**
   ```bash
   git checkout main
   git pull origin main
   ```

2. **Verify tests pass**
   ```bash
   make test
   make lint
   ```

3. **Create and push a version tag**
   ```bash
   # Use semantic versioning (v1.0.0, v1.2.3, etc.)
   git tag v1.0.0
   git push origin v1.0.0
   ```

4. **Monitor the release**
   - Go to: https://github.com/nasty-project/nasty-csi/actions
   - Watch the "Release" workflow run
   - The workflow will:
     - Run tests and linters
     - Build multi-arch Docker images (amd64, arm64)
     - Push images to Docker Hub and GitHub Container Registry
      - Create GitHub release with changelog

5. **Verify release artifacts**
   - Docker Hub: https://hub.docker.com/r/bfenski/nasty-csi
   - GitHub Releases: https://github.com/nasty-project/nasty-csi/releases
    - GHCR: https://github.com/orgs/nasty-project/packages

## What Gets Published

Each release creates the following artifacts:

### Docker Images
- **Docker Hub**: `bfenski/nasty-csi:v1.0.0`, `bfenski/nasty-csi:v1.0`, `bfenski/nasty-csi:v1`, `bfenski/nasty-csi:latest`
- **GHCR**: `ghcr.io/nasty-project/nasty-csi:v1.0.0`, etc.
- **Architectures**: linux/amd64, linux/arm64

### GitHub Release
- Automatic changelog from git commits
- Installation instructions
- Links to Docker images and the separately released Helm chart

## Version Tagging Strategy

We follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version (v1.0.0 → v2.0.0): Breaking changes
- **MINOR** version (v1.0.0 → v1.1.0): New features, backwards compatible
- **PATCH** version (v1.0.0 → v1.0.1): Bug fixes, backwards compatible

### Examples

```bash
# First release
git tag v1.0.0
git push origin v1.0.0

# Bug fix release
git tag v1.0.1
git push origin v1.0.1

# New feature release
git tag v1.1.0
git push origin v1.1.0

# Breaking change release
git tag v2.0.0
git push origin v2.0.0
```

## Development Builds

The manually triggered CI workflow builds and pushes development images:

- **Docker Hub**: `bfenski/nasty-csi:latest`
- **GHCR**: `ghcr.io/nasty-project/nasty-csi:latest`

These are useful for testing but should **not** be used in production.

## Helm Chart Versioning

The chart is released independently from
[nasty-project/nasty-chart](https://github.com/nasty-project/nasty-chart). Its
`appVersion` selects a published CSI image version.

## Testing a Release

After publishing a release, test it:

```bash
# Test Docker image
docker pull bfenski/nasty-csi:v1.0.0
docker run --rm bfenski/nasty-csi:v1.0.0 --version

# Inspect the matching GHCR image
docker buildx imagetools inspect ghcr.io/nasty-project/nasty-csi:v1.0.0
```

## Troubleshooting

### Release workflow fails on Docker push

**Error**: `denied: requested access to the resource is denied`

**Solution**: 
1. Verify `DOCKERHUB_USERNAME` secret matches your Docker Hub username exactly
2. Verify `DOCKERHUB_TOKEN` is a valid access token (not password)
3. Regenerate token if needed: https://hub.docker.com/settings/security

### Tag already exists

**Error**: `tag 'v1.0.0' already exists`

**Solution**:
```bash
# Delete local tag
git tag -d v1.0.0

# Delete remote tag
git push --delete origin v1.0.0

# Recreate tag at current commit
git tag v1.0.0
git push origin v1.0.0
```

### Multi-arch build fails

**Error**: Platform build failures

**Solution**: The workflow uses GitHub-hosted runners which support multi-arch builds via QEMU. If builds are slow or fail, consider:
1. Using self-hosted runners with native arm64 support
2. Removing arm64 from the release workflow temporarily

## Manual Release (Emergency)

If GitHub Actions is unavailable, you can release manually:

```bash
# 1. Set version
VERSION=v1.0.0

# 2. Check out the pinned local dependency
git clone https://github.com/nasty-project/nasty-go.git nasty-go
git -C nasty-go checkout 9c384afe026f40355a344400388688ba9a0789d8

# 3. Build and push Docker images
COMMIT=$(git rev-parse --short HEAD)
BUILD_DATE=$(git show -s --format=%cI HEAD)
MINOR=${VERSION%.*}
MAJOR=${VERSION%%.*}
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION="${VERSION}" \
  --build-arg GIT_COMMIT="${COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}" \
  -t "bfenski/nasty-csi:${VERSION}" \
  -t "bfenski/nasty-csi:${MINOR}" \
  -t "bfenski/nasty-csi:${MAJOR}" \
  -t "bfenski/nasty-csi:latest" \
  -t "ghcr.io/nasty-project/nasty-csi:${VERSION}" \
  -t "ghcr.io/nasty-project/nasty-csi:${MINOR}" \
  -t "ghcr.io/nasty-project/nasty-csi:${MAJOR}" \
  -t "ghcr.io/nasty-project/nasty-csi:latest" \
  --push .

# 4. Create the GitHub release after both registries contain the image
# https://github.com/nasty-project/nasty-csi/releases/new
```

## References

- [Semantic Versioning](https://semver.org/)
- [Docker Hub OCI Support](https://docs.docker.com/docker-hub/oci-artifacts/)
- [Helm OCI Registries](https://helm.sh/docs/topics/registries/)
- [GitHub Actions Workflows](.github/workflows/)
