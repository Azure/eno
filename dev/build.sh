#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

export TAG="$(date +%s)"

function build() {
    cmd=$(basename $1)
    docker build --quiet -t "$REGISTRY/$cmd:$TAG" -f "$f/Dockerfile" .
    [[ -z "${SKIP_PUSH}" ]] && docker push "$REGISTRY/$cmd:$TAG"
}

# Build!
for f in docker/*; do
    build $f &
done
wait

# Deploy!
cat "$(dirname "$0")/deploy.yaml" | envsubst | kubectl apply -f - -f ./api/v1/config/crd
echo "Success! You're running tag: $TAG"
