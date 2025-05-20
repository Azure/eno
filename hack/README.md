# Development Scripts

This directory contains scripts for development workflows with the Eno project.

## Available Scripts

- `build.sh` - Builds and pushes all Docker images in the docker/ directory and deploys to a Kubernetes cluster
- `build-linux.sh` - Similar to build.sh but specifically builds for linux/amd64 platform in parallel
- `deploy.yaml` - Kubernetes deployment manifest used by the build scripts
- `smoke-test.sh` - Script to test the Eno application by applying example resources and verifying reconciliation

## Usage

Most scripts can be run directly, but it's recommended to use the Makefile targets for a consistent interface:

```bash
# Build and deploy all images
make build

# Build images specifically for Linux
make build-linux

# Run a smoke test
make smoke-test

# Build specific controller images
make docker-build-eno-controller
make docker-build-eno-reconciler
```

Environment variables:

- `REGISTRY`: Required. Specifies the container registry to push images to.
- `TAG`: Optional. Override the automatically generated tag for images.
- `SKIP_PUSH`: Optional. If set, skip pushing images to the registry.