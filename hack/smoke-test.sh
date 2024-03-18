#!/bin/bash

# Wait for apiserver to be ready
while true; do
    kubectl api-resources
    if [[ $? -eq 0 ]]; then
        break
    else
        sleep 1
    fi
done

# Start Eno
kubectl apply -f api/v1/config/crd
go run ./cmd/eno-controller --health-probe-addr=:0 --metrics-addr=:0 --synthesizer-pod-namespace=default &
go run ./cmd/eno-reconciler --health-probe-addr=:0 --metrics-addr=:0 &

# Apply example
kubectl apply -f ./examples/simple.yaml

# Wait for the composition to be reconciled
while true; do
    json=$(kubectl get composition test-comp -o=json)
    echo "${json}"

    echo $json | jq --exit-status '.status.currentSynthesis.ready'
    if [[ $? -eq 0 ]]; then
        break
    else
        sleep 1
    fi
done

# Delete the example and wait for cleanup
kubectl delete composition test-comp --wait=true --timeout=1m

# Clean up controller background jobs
kill $(jobs -p)
