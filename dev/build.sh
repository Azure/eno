#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

export TAG="$(date +%s)"

function build() {
    cmd=$(basename $1)
    docker build -t "$REGISTRY/$cmd:$TAG" -f "$f/Dockerfile" .
    if [[ -z "${SKIP_PUSH}" ]]; then
        docker push "$REGISTRY/$cmd:$TAG"
    fi
}

# Build!
for f in docker/*; do
    build $f
done

# Deploy!
export DISABLE_SSA="${DISABLE_SSA:=false}"
cat "$(dirname "$0")/deploy.yaml" | envsubst | kubectl apply -f - -f ./api/v1/config/crd
echo "Success! You're running tag: $TAG"
