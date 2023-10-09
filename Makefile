IMAGE_PREFIX ?= replaceme
IMAGES := $(wildcard images/*)
NOW := $(shell date +%s)

all: $(IMAGES) images/tags.env

.PHONY: $(IMAGES)
$(IMAGES):
	docker build -t $(IMAGE_PREFIX)/$(notdir $@):$(NOW) -f $@/Dockerfile .
	docker push $(IMAGE_PREFIX)/$(notdir $@):$(NOW)
	
images/tags.env:
	@echo > $@
	@echo "export CONTROLLER_IMAGE=$(IMAGE_PREFIX)/eno-controller:$(NOW)" >> $@
	@echo "export WRAPPER_IMAGE=$(IMAGE_PREFIX)/eno-wrapper:$(NOW)" >> $@

.PHONY: deploy
deploy:
	. images/tags.env && envsubst < dev.yaml | kubectl apply  -f ./api/v1/config/crd -f -
