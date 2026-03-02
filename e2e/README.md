# End-to-End Tests

This directory contains e2e tests that run against a live Kubernetes cluster with
Eno's controllers deployed. They verify full lifecycle flows — creating
Synthesizers/Compositions, waiting for synthesis, checking outputs, updating,
and deleting.

## Prerequisites

- A running Kubernetes cluster with Eno controllers deployed.
- `KUBECONFIG` set (or in-cluster config available).

## Running E2E Tests

```bash
# Run all e2e tests
make test-e2e
```



## Test Structure
Each test file (`*_test.go`) defines a workflow as a **directed acyclic graph
(DAG)** that can run  no dependency steps in parallel automatically.

A test follows three phases:

### 1. Define resources and variables

```go
cli := fw.NewClient(t)
synthName := fw.UniqueName("my-synth")
synth := fw.NewMinimalSynthesizer(synthName, fw.WithCommand(fw.ToCommand(cm)))
comp  := fw.NewComposition(compName, "default", fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}))
```

### 2. Define steps

Use framework helpers for common operations:

```go
createSynth := fw.CreateStep(t, "createSynth", cli, synth)      // creates a resource
deleteSynth := fw.DeleteStep(t, "deleteSynth", cli, synth)      // deletes a resource
cleanup     := fw.CleanupStep(t, "cleanup", cli, synth, comp)   // deletes + waits for NotFound
```

For custom logic, use `flow.Func`:

```go
verify := flow.Func("verify", func(ctx context.Context) error {
    fw.WaitForResourceExists(t, ctx, cli, &cm, 30*time.Second)
    assert.Equal(t, "expected", cm.Data["key"])
    return nil
})
```

### 3. Wire the DAG and execute

```go
w := new(flow.Workflow)
w.Add(
    flow.Step(createComp).DependsOn(createSynth),   // sequential
    flow.Step(waitReady).DependsOn(createComp),
    flow.Step(verifyA).DependsOn(waitReady),         // verifyA and verifyB
    flow.Step(verifyB).DependsOn(waitReady),         // run in parallel
    flow.Step(cleanup).DependsOn(verifyA, verifyB),  // waits for both
)
require.NoError(t, w.Do(ctx))
```

## Framework Utilities (`e2e/framework/`)

| File | Contents |
|------|----------|
| `framework.go` | `NewClient` — creates a controller-runtime client from KUBECONFIG. |
| `crud.go` | Resource builders (`NewMinimalSynthesizer`, `NewComposition`, `NewSymphony`), `ToCommand` for producing synthesizer commands, and workflow step helpers (`CreateStep`, `DeleteStep`, `CleanupStep`). |
| `testutils.go` | Polling helpers: `WaitForCompositionReady`, `WaitForCompositionResynthesized`, `WaitForSymphonyReady`, `WaitForResourceExists`, `WaitForResourceDeleted`. |

## CI Pipeline

The **E2E Tests** workflow (`.github/workflows/e2e.yaml`) runs automatically on
every push and on pull requests targeting `main`. It also runs on a daily
schedule. No manual action is needed — opening a PR will trigger it.

The pipeline:

1. Creates a Kind cluster and deploys Eno into it.
2. The **"Run E2E tests"** output test names, pass/fail status, and log lines so you can
   follow progress directly in the GitHub Actions log.
3. If any test fails, a **"Dump diagnostics"** step runs automatically and
   prints the following to the job output:
   - Controller and Reconciler pod logs 
   - Kubernetes events
   - Full YAML of all Compositions, Synthesizers, and ResourceSlices
   - Pods across all namespaces

## Adding a New Test

1. Create a `*_test.go` file in `e2e/` (package `e2e`).
2. Build resources with the `fw.*` helpers or manually construct them.
3. Define steps — prefer `fw.CreateStep`/`fw.DeleteStep`/`fw.CleanupStep` for
   CRUD and `flow.Func` for assertions and custom logic.
4. Wire them into a DAG with `flow.Step(...).DependsOn(...)`.
5. Call `w.Do(ctx)` and assert no error.
6. Always clean up created resources at the end of the DAG.
