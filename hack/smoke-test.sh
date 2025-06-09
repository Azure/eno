#!/bin/bash

set -e

# Apply examples
for file in ./examples/*/example.yaml; do
    kubectl apply -f $file
done

set +e

# Tail the controller logs
function watch_logs() {
    while true; do
        kubectl logs -f -l $1
        sleep 1
    done
}
watch_logs app=eno-controller &
watch_logs app=eno-reconciler &

# Wait for the composition to be reconciled
while true; do
    output=$(kubectl get compositions --no-headers)
    echo $output

    if echo "$output" | grep -qv "Ready"; then
        sleep 1
    else
        break
    fi
done

set -e

# Delete the example and wait for cleanup
kubectl delete composition --all --wait=true --timeout=1m
