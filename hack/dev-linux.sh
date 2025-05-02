#!/bin/bash

set -e

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

export TAG="$(date +%s)"

function build() {
    cmd=$(basename $1)
    docker build --platform=linux/amd64 --quiet -t "$REGISTRY/$cmd:$TAG" -f "$f/Dockerfile" .
    [[ -z "${SKIP_PUSH}" ]] && docker push "$REGISTRY/$cmd:$TAG"
}

# Build!
for f in docker/*; do
    build $f &
done
wait

echo "Success! built and pushed tag: $TAG"
