ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif

ENO_CONTROLLER_IMAGE_VERSION ?= $(TAG)
ENO_CONTROLLER_IMAGE_NAME ?= eno-controller
ENO_RECONCILER_IMAGE_VERSION ?= $(TAG)
ENO_RECONCILER_IMAGE_NAME ?= eno-reconciler

.PHONY: docker-build-eno-controller
docker-build-eno-controller:
	DOCKER_BUILDKIT=1 docker build \
		--file docker/$(ENO_CONTROLLER_IMAGE_NAME)/Dockerfile \
		--tag $(REGISTRY)/$(ENO_CONTROLLER_IMAGE_NAME):$(ENO_CONTROLLER_IMAGE_VERSION) .
	docker push $(REGISTRY)/$(ENO_CONTROLLER_IMAGE_NAME):$(ENO_CONTROLLER_IMAGE_VERSION)

.PHONY: docker-build-eno-reconciler
docker-build-eno-reconciler:
	DOCKER_BUILDKIT=1 docker build \
		--file docker/$(ENO_RECONCILER_IMAGE_NAME)/Dockerfile \
		--tag $(REGISTRY)/$(ENO_RECONCILER_IMAGE_NAME):$(ENO_RECONCILER_IMAGE_VERSION) .
	docker push $(REGISTRY)/$(ENO_RECONCILER_IMAGE_NAME):$(ENO_RECONCILER_IMAGE_VERSION)

# Setup controller-runtime test environment binaries
.PHONY: setup-testenv
setup-testenv:
	@echo "Installing controller-runtime testenv binaries..."
	@go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path

.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object crd paths="./..." output:crd:artifacts:config=api/v1/config/crd

.PHONY: test
test:
	UPSTREAM_KUBEBUILDER_ASSETS=$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path) go test -v $$(go list ./... | grep -v '/e2e')

.PHONY: test-e2e
test-e2e:
	go test -v -timeout 10m -count=1 ./e2e

# find or download controller-gen
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0 ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(shell go env GOPATH)/bin/controller-gen
else
CONTROLLER_GEN=$(shell which controller-gen)
endif
