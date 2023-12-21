ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif
ENO_MANAGER_IMAGE_VERSION ?= $(TAG)
ENO_MANAGER_IMAGE_NAME ?= eno-manager

# Images
OUTPUT_TYPE ?= type=registry

.PHONY: docker-build-eno-manager
docker-build-hub-agent: docker-buildx-builder
	docker buildx build \
		--file docker/$(ENO_MANAGER_IMAGE_NAME)/Dockerfile \
		--output=$(OUTPUT_TYPE) \
		--platform="linux/amd64" \
		--pull \
		--tag $(REGISTRY)/$(ENO_MANAGER_IMAGE_NAME):$(ENO_MANAGER_IMAGE_VERSION) .
