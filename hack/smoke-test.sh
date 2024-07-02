#!/bin/bash

set -e

# Apply examples
for file in ./examples/*/example.yaml; do
    kubectl apply -f $file
done

set +e

# Wait for the composition to be reconciled
while true; do
    output=$(kubectl get compositions --no-headers)
    echo $output

    echo $output | awk '{ if ($0 !~ "Ready") exit 1 }'
    if [[ $? -eq 0 ]]; then
        break
    else
        sleep 1
    fi
done

set -e

# Delete the example and wait for cleanup
kubectl delete composition --all --wait=true --timeout=1m
