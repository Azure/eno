ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif

ENO_CONTROLLER_IMAGE_VERSION ?= $(TAG)
ENO_CONTROLLER_IMAGE_NAME ?= eno-controller
ENO_RECONCILER_IMAGE_VERSION ?= $(TAG)
ENO_RECONCILER_IMAGE_NAME ?= eno-reconciler

# Build and deploy targets that use scripts in hack/ directory
.PHONY: build
build:
	@hack/build.sh

.PHONY: build-linux
build-linux:
	@hack/build-linux.sh

.PHONY: smoke-test
smoke-test:
	@hack/smoke-test.sh

# Individual image build targets
.PHONY: docker-build-eno-controller
docker-build-eno-controller:
	docker build \
		--file docker/$(ENO_CONTROLLER_IMAGE_NAME)/Dockerfile \
		--tag $(REGISTRY)/$(ENO_CONTROLLER_IMAGE_NAME):$(ENO_CONTROLLER_IMAGE_VERSION) .
	docker push $(REGISTRY)/$(ENO_CONTROLLER_IMAGE_NAME):$(ENO_CONTROLLER_IMAGE_VERSION)

.PHONY: docker-build-eno-reconciler
docker-build-eno-reconciler:
	docker build \
		--file docker/$(ENO_RECONCILER_IMAGE_NAME)/Dockerfile \
		--tag $(REGISTRY)/$(ENO_RECONCILER_IMAGE_NAME):$(ENO_RECONCILER_IMAGE_VERSION) .
	docker push $(REGISTRY)/$(ENO_RECONCILER_IMAGE_NAME):$(ENO_RECONCILER_IMAGE_VERSION)
