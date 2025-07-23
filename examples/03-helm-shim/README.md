# Helm Shim Example

A simple example for the helm-shim library.

## Concept

Helm charts are packaged into the synthesizer container image, and executed into their final YAML representation using the upstream Helm libraries.
The final, non-templated resources are then transformed into the KRM ResourceList representation and handed to Eno.

## Caveats

- Hooks are not supported, although most use-cases are possible using various `eno.azure.io` annotations

## Files

- `main.go`: Transform Eno inputs into Helm values, execute the chart
- `chart/`: A minimal but otherwise standard Helm chart
- `example.yaml`: An example Composition that uses the helm-shim synth
- `build-sh.sh`: Build/push/deploy script for main.go - creates the Synthesizer CR
