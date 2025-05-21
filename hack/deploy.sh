#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

if [[ -z "${TAG}" ]]; then
    echo "TAG must be set" > /dev/stderr
    exit 1
fi

# Deploy!
cat "$(dirname "$0")/deploy.yaml" | envsubst | kubectl apply -f - -f ./api/v1/config/crd
echo "Success! You're running tag: $TAG"