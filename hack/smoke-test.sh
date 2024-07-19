#!/bin/bash

set -e

# Apply examples
for file in ./examples/*/example.yaml; do
    kubectl apply -f $file
done

set +e

# Wait for the composition to be reconciled
counter=0
while true; do
    output=$(kubectl get compositions --no-headers)
    echo $output

    if echo "$output" | grep -qv "Ready"; then
        sleep 1
    else
        break
    fi

    ((counter++))
    if ((counter % 30 == 0)); then
        echo "---- controller logs"
        kubectl logs -l app=eno-controller
    fi
done

set -e

# Delete the example and wait for cleanup
kubectl delete composition --all --wait=true --timeout=1m
