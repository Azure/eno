- Implements slow rollout for generator updates
- Remove `kubectl apply` patch annotation if it is invalid
- Fix readiness controller
- Two-way Helm ownership migration

Consider:

- Configmap/secret reconcile ordering?
- Custom ordering using annotations?
- How to support partial reconciliation?
- Rename GeneratedResource to ManagedResource?
