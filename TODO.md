- Generator CRD for configuring the generation runtime (support slow rollout between versions)
- Switch to client-side patching w/ cached openapi discovery
- More readiness support (conditions matcher for CRs, more core resources)
- Two-way Helm ownership migration
- Reconcile ordering using depedency annotations (wait for readiness)
- Reconcile partitioning
- Reconcile prioritization
- Expose leader election and other controller settings

- Configmap/secret reconcile ordering?
