#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

TAG="$(date +%s)"
export IMAGE="$REGISTRY/example-helm-shim:$TAG"

docker build --quiet -t ${IMAGE} -f "examples/3-helm-shim/Dockerfile" .
[[ -z "${SKIP_PUSH}" ]] && docker push ${IMAGE}

kubectl apply -f - <<YAML
    apiVersion: eno.azure.io/v1
    kind: Synthesizer
    metadata:
      name: helm-shim-example
    spec:
      image: $IMAGE
      refs:
        - key: myinput
          resource:
            group: "" # core
            version: "v1"
            kind: ConfigMap
YAML
