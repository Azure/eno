# Hack Scripts

This directory contains development and utility scripts for the Eno project.

## Development Scripts

### Building and Deploying

- **build.sh**: Build Docker images for Eno components
  ```bash
  # Build all images (requires REGISTRY env var)
  ./hack/build.sh
  
  # Build specific component
  FILTER=eno-controller ./hack/build.sh
  
  # Build for linux/amd64 in parallel
  ./hack/build.sh --platform=linux/amd64 --parallel
  
  # Build and deploy
  ./hack/build.sh --deploy
  
  # Skip pushing images to registry
  ./hack/build.sh --skip-push
  ```

- **deploy.sh**: Deploy Eno components to a Kubernetes cluster
  ```bash
  # Deploy (requires REGISTRY and TAG env vars)
  ./hack/deploy.sh
  ```

### Testing

- **smoke-test.sh**: Run a smoke test on the deployed Eno components
  ```bash
  ./hack/smoke-test.sh
  ```

## Kubernetes Utilities

- **build-k8s-matrix.sh**: Generate a matrix of Kubernetes versions for testing
  ```bash
  ./hack/build-k8s-matrix.sh
  ```

- **download-k8s.sh**: Download a specific version of Kubernetes binaries
  ```bash
  # Download latest version
  ./hack/download-k8s.sh
  
  # Download specific minor version
  ./hack/download-k8s.sh 23
  ```

## Using the Makefile

For convenience, all common development tasks are available as Makefile targets:

```bash
# Build all images
make build

# Deploy using existing images
make deploy

# Build and deploy
make build-deploy

# Build for linux/amd64 in parallel
make build-linux

# Build specific components
make docker-build-eno-controller
make docker-build-eno-reconciler

# Run smoke tests
make smoke-test

# Setup test environment
make setup-testenv
```