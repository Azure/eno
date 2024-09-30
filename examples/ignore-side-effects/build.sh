#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

TAG="$(date +%s)"
export IMAGE="$REGISTRY/example-go-synthesizer-side-effects:$TAG"

docker build --quiet -t ${IMAGE} -f "examples/ignore-side-effects/Dockerfile" .
[[ -z "${SKIP_PUSH}" ]] && docker push ${IMAGE}

kubectl apply -f - <<YAML
    apiVersion: eno.azure.io/v1
    kind: Synthesizer
    metadata:
      name: go-synth-example-side-effects
    spec:
      image: $IMAGE
      refs:
        - key: example-input
          resource:
            group: "" # core
            version: "v1"
            kind: ConfigMap
YAML
