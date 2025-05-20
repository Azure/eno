ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif

ENO_CONTROLLER_IMAGE_VERSION ?= $(TAG)
ENO_CONTROLLER_IMAGE_NAME ?= eno-controller
ENO_RECONCILER_IMAGE_VERSION ?= $(TAG)
ENO_RECONCILER_IMAGE_NAME ?= eno-reconciler

# The default test timeout (10m for normal tests)
TEST_TIMEOUT ?= 10m

# Default Kubernetes version for local development
K8S_VERSION ?= 1.28.x

.PHONY: test
test:
	go test -timeout $(TEST_TIMEOUT) ./...

.PHONY: test-reconciliation
test-reconciliation:
	go test -timeout $(TEST_TIMEOUT) ./internal/controllers/reconciliation

.PHONY: install-envtest
install-envtest:
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	setup-envtest use $(K8S_VERSION) -p path

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
