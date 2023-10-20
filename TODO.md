- Implement slow rollout for generator updates
  - Add revision to generator CRD for no-change rollouts (tracking external state)
- Remove `kubectl apply` patch annotation if it is invalid
- Fix readiness controller
- Two-way Helm ownership migration

Consider:

- Configmap/secret reconcile ordering?
- Custom ordering using annotations?
- How to support partial reconciliation?
- Rename GeneratedResource to ManagedResource?
