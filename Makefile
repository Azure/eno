ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif

# All build and deployment tasks have been consolidated into scripts in the hack directory
# Use the Makefile targets below to access them

# Build all images
.PHONY: build
build:
	@./hack/build.sh

# Deploy only (requires existing images)
.PHONY: deploy
deploy:
	@./hack/deploy.sh

# Build all images and deploy
.PHONY: build-deploy
build-deploy:
	@./hack/build.sh --deploy

# Build all images in parallel (for Linux/AMD64)
.PHONY: build-linux
build-linux:
	@./hack/build.sh --platform=linux/amd64 --parallel

# Build individual components
.PHONY: docker-build-eno-controller
docker-build-eno-controller:
	@FILTER=eno-controller ./hack/build.sh

.PHONY: docker-build-eno-reconciler
docker-build-eno-reconciler:
	@FILTER=eno-reconciler ./hack/build.sh

# Run smoke tests
.PHONY: smoke-test
smoke-test:
	@./hack/smoke-test.sh

# Setup controller-runtime test environment binaries
.PHONY: setup-testenv
setup-testenv:
	@echo "Installing controller-runtime testenv binaries..."
	@go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path
