#!/bin/bash
# Script to deploy Eno components

set -e

show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo "Deploy Eno components"
    echo ""
    echo "Options:"
    echo "  --help   Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  TAG       Tag for images to deploy (required)"
    echo "  REGISTRY  Registry to pull images from (required)"
}

# Parse arguments
for arg in "$@"; do
    case $arg in
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

if [[ -z "${TAG}" ]]; then
    echo "TAG must be set" > /dev/stderr
    exit 1
fi

# Get the script directory and repository root
SCRIPT_DIR=$(dirname "$(readlink -f "$0")")
ROOT_DIR=$(dirname "$SCRIPT_DIR")

# Check if deploy.yaml exists
DEPLOY_FILE="$SCRIPT_DIR/deploy.yaml"
if [ ! -f "$DEPLOY_FILE" ]; then
    echo "Error: deploy.yaml not found at $DEPLOY_FILE"
    exit 1
fi

echo "Deploying Eno with registry: $REGISTRY and tag: $TAG"

# Apply the deployment
cat "$DEPLOY_FILE" | envsubst | kubectl apply -f - -f "$ROOT_DIR/api/v1/config/crd"

echo "Deployment completed successfully"