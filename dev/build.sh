#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

export TAG="$(date +%s)"

function build() {
    cmd=$(basename $1)
    buildah build -t "$REGISTRY/$cmd:$TAG" -f "$f/Dockerfile"
    buildah push "$REGISTRY/$cmd:$TAG"
}

# Build!
for f in cmd/*; do
    build $f &
done
wait

# Deploy!
cat "$(dirname "$0")/deploy.yaml" | envsubst | kubectl apply -f -
echo "Success! You're running tag: $TAG"
