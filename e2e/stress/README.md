# Eno stress runner

`eno-stress` is a standalone binary for running YAML-defined workloads against a live Kubernetes cluster. It is stored under `e2e/`, but it is not invoked by the Eno e2e test suite.

## Build

Build the standalone client binary from the repository root:

```bash
mkdir -p bin
go build -o bin/eno-stress ./e2e/stress/cmd/eno-stress
```

Build and push the synthesizer image to a registry reachable by the target cluster:

```bash
docker build \
  -f e2e/stress/synthesizer/Dockerfile \
  -t REGISTRY/eno-stress-synthesizer:TAG .
docker push REGISTRY/eno-stress-synthesizer:TAG
```

The ACR image is referenced by `spec.run.variables.synthesizerImage` in
`e2e/stress/plans/missing-input-fanout/plan.yaml`:

```yaml
spec:
  run:
    variables:
      synthesizerImage: MYACR.azurecr.io/eno-stress-synthesizer:TAG
```

The Synthesizer template consumes that value as `spec.image`:

```yaml
spec:
  image: "${synthesizerImage}"
```

The target cluster must be able to pull the image from ACR, either through its
managed identity or its configured image-pull credentials.

## Completion stages

Set `spec.run.completionStage` to control when `run` stops and writes its
report:

```yaml
spec:
  run:
    completionStage: PreSynthesis
```

- `PreSynthesis`: every Composition has observed the acknowledged input
  revisions and exited `MissingInputs`.
- `PostSynthesis`: `PreSynthesis` plus every Composition has a non-empty
  `status.*Synthesis.synthesized` timestamp.
- `Reconcile`: `PostSynthesis` plus every Composition has a non-empty
  `status.currentSynthesis.reconciled` timestamp. This does not wait for
  resource readiness.

Later stages include all measurements from earlier stages. `Reconcile` is the
default when `completionStage` is omitted.

## Reusable setup resources

Setup resources can opt into reuse:

```yaml
setup:
  resources:
    - name: synthesizer
      reuse: true
```

When a reusable resource already exists, the runner performs a server-side
dry-run update and compares the resulting `spec` with the existing `spec`. An
unchanged resource is reused without relabeling or modifying it. A changed
image, command, ref, or other spec field fails preparation explicitly.

## Namespace naming

The included plan sets `namespacePrefix: 6a`. Each namespace is named `6a`
followed immediately by 16 lowercase hexadecimal characters, for example
`6a9e3a86214e85de76`. The suffix is deterministically derived from the run ID
and namespace index so all 150 namespaces are unique and resuming `prepare`
uses the same names.

## Symphony topology

For every generated namespace, `prepare` creates one Symphony from
`plans/missing-input-fanout/manifests/symphony.yaml`. The Symphony has 20
variations referencing 20 Synthesizer objects that all use the same image.
Every variation inherits the same three input bindings and creates one unique
output ConfigMap. This produces 3,000 Compositions and outputs across 150
namespaces. The plan declares the generated Compositions with
`operation: observe`, so the runner records controller-generated objects
instead of creating duplicate Compositions.

The 20-variation plan uses state schema `v1alpha2`. Do not reuse a state file
created by an older stress binary or by the one-Composition plan.

## Run against a live cluster

```bash
bin/eno-stress validate \
  --plan e2e/stress/plans/missing-input-fanout/plan.yaml \
  --kubeconfig "$KUBECONFIG"

bin/eno-stress prepare \
  --plan e2e/stress/plans/missing-input-fanout/plan.yaml \
  --state /tmp/eno-stress-state.json \
  --kubeconfig "$KUBECONFIG"

bin/eno-stress run \
  --state /tmp/eno-stress-state.json \
  --kubeconfig "$KUBECONFIG"
```

`prepare` creates namespaces, the Synthesizer, and Compositions, then waits for every Composition to report `MissingInputs`. It never creates test inputs. `run` reloads the state, verifies that Compositions remain in `MissingInputs`, establishes watches, and only then creates inputs.

Use `status` to inspect partial results and `cleanup` to delete only the test namespaces recorded in state. Namespace deletion cascades namespaced Symphonies, Compositions, inputs, and outputs. Cluster-scoped Synthesizers are retained. Cleanup requires both the recorded run label and UID before deleting a namespace.

```bash
bin/eno-stress status --state /tmp/eno-stress-state.json
bin/eno-stress cleanup --state /tmp/eno-stress-state.json --kubeconfig "$KUBECONFIG"
```

The generated JSON report is written relative to the plan directory unless `metrics.reportFile` is absolute. The state file is written atomically with mode `0600` and should not be committed.

## Repeated cycles

`run-cycles.sh` validates once, waits for all generated `6a<16 hex>`
namespaces to disappear, and runs `prepare`, `run`, namespace-only `cleanup`,
and deletion waiting for each cycle. Full command output is appended to
`results.txt`, and each state file is moved to `state-history/`.

```bash
CYCLES=5 ./run-cycles.sh
```

For a session that should survive disconnects:

```bash
nohup env CYCLES=5 ./run-cycles.sh >> cycle-console.log 2>&1 &
echo $! > cycle.pid
```

Settings can be overridden with `CYCLES`, `WAIT_SECONDS`, `KUBECONFIG`,
`ENO_STRESS`, `PLAN`, `STATE`, `RESULTS_FILE`, and `STATE_HISTORY`.