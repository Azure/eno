#!/bin/bash
# Consolidated build script for Eno components

set -e

show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo "Build and optionally deploy Eno components"
    echo ""
    echo "Options:"
    echo "  --platform=PLATFORM  Set platform (default: host, options: linux/amd64)"
    echo "  --skip-push          Skip pushing images to registry"
    echo "  --parallel           Build images in parallel"
    echo "  --deploy             Deploy after building"
    echo "  --help               Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  REGISTRY             Registry to push images to (required)"
    echo "  TAG                  Tag for images (default: current timestamp)"
    echo "  FILTER               Only build components matching this filter"
}

# Default values
PLATFORM=""
SKIP_PUSH=""
PARALLEL=false
DEPLOY=false

# Parse arguments
for arg in "$@"; do
    case $arg in
        --platform=*)
        PLATFORM="${arg#*=}"
        shift
        ;;
        --skip-push)
        SKIP_PUSH="true"
        shift
        ;;
        --parallel)
        PARALLEL=true
        shift
        ;;
        --deploy)
        DEPLOY=true
        shift
        ;;
        --help)
        show_help
        exit 0
        ;;
        *)
        # Unknown option
        echo "Unknown option: $arg"
        show_help
        exit 1
        ;;
    esac
done

if [[ -z "${REGISTRY}" ]]; then
    echo "REGISTRY must be set" > /dev/stderr
    exit 1
fi

# Use git commit hash if TAG is not set
if [[ -z "${TAG}" ]]; then
    export TAG="$(date +%s)"
fi

function build() {
    cmd=$(basename $1)
    
    # Skip if filter is set and doesn't match
    if [[ ! -z "${FILTER}" ]] && [[ ! "$cmd" =~ ${FILTER} ]]; then
        return
    fi
    
    echo "Building $cmd..."
    
    build_cmd="docker build"
    if [[ ! -z "${PLATFORM}" ]]; then
        build_cmd="$build_cmd --platform=${PLATFORM}"
    fi
    
    $build_cmd -t "$REGISTRY/$cmd:$TAG" -f "$1/Dockerfile" .
    
    if [[ -z "${SKIP_PUSH}" ]]; then
        echo "Pushing $REGISTRY/$cmd:$TAG..."
        docker push "$REGISTRY/$cmd:$TAG"
    fi
    
    echo "Finished $cmd"
}

echo "Building images with tag: $TAG"

# Build all components
if [ "$PARALLEL" = true ]; then
    echo "Building in parallel..."
    for f in docker/*; do
        build $f &
    done
    wait
else
    for f in docker/*; do
        build $f
    done
fi

echo "All images built successfully"

# Deploy if requested
if [ "$DEPLOY" = true ]; then
    echo "Deploying..."
    SCRIPT_DIR=$(dirname "$(readlink -f "$0")")
    
    if [ -f "$SCRIPT_DIR/deploy.yaml" ]; then
        cat "$SCRIPT_DIR/deploy.yaml" | envsubst | kubectl apply -f - -f "$SCRIPT_DIR/../api/v1/config/crd"
        echo "Deployment completed"
    else
        echo "Warning: deploy.yaml not found in hack directory"
        exit 1
    fi
fi

echo "Success! Images built with tag: $TAG"