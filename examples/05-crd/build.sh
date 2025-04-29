#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

TAG="$(date +%s)"
export IMAGE="$REGISTRY/crd-synthesizer:$TAG"

docker build --quiet -t ${IMAGE} -f "examples/05-crd/Dockerfile" .
[[ -z "${SKIP_PUSH}" ]] && docker push ${IMAGE}

kubectl apply -f - <<YAML
    apiVersion: eno.azure.io/v1
    kind: Synthesizer
    metadata:
      name: crd-example
    spec:
      image: $IMAGE
YAML
