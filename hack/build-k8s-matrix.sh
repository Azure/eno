#!/bin/bash

set -e

start_minor=10
latest=$(curl -sL https://dl.k8s.io/release/stable.txt) # e.g. "v1.33.1"
latest_minor=$(echo "$latest" | cut -d. -f2)
seq $start_minor $latest_minor | jq --raw-input --slurp -c 'split("\n") | map(select(. != ""))'
