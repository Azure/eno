# Resource Migration Between Compositions

> ⚠️ This is an advanced Eno concept that builds on [Symphonies](./symphony.md), [Overrides](./overrides.md), and [Reconciliation](./reconciliation.md)

Resource migration enables you to safely transfer ownership of resources from one composition to another within the same symphony. This is useful when reorganizing resource management, splitting monolithic compositions, or consolidating resources.

## Why Migration is Complex

When two compositions manage the same resource simultaneously, they can conflict and create oscillating updates. Eno's Symphony annotation reconciliation protocol prevents this by ensuring annotations exist on **at most one** composition at a time.

## Migration Process

> ⚠️ It's important that the each step is deployed before continuing to the next step.

### Step 1: Prepare the Symphony

```yaml
apiVersion: eno.azure.io/v1
kind: Symphony
metadata:
  name: my-symphony
  namespace: default
spec:
  variations:
    # Source composition currently managing the resource
    - synthesizer:
        name: source-synth
      annotations:
        # You can pick any annotation key, we'll use this one as an example.
        # The value set here signals that the current synth should continue to own the resource (for now).
        migration/resource-owner: "true"

    # At this point, including the migration's target synth is optional.
    # If it already exists in the symphony, update it to explicitly NOT take ownership of the migrated resource(s).
    - synthesizer:
        name: target-synth
      annotations:
        migration/resource-owner: ""
```

### Step 2: Configure Conditional Reconciliation

Update both synthesizers to conditionally skip reconciliation based on the migration annotation. This prevents conflicts during the handoff.

**Example using overrides:**

```yaml
# Added to the resource by both synthesizers
metadata:
  annotations:
    eno.azure.io/overrides: |
      [
        {
          "path": "self.metadata.annotations['eno.azure.io/disable-reconciliation']",
          "value": "true",
          "condition": "composition.metadata.annotations['migration/resource-owner'] != 'true'"
        }
      ]
```

This configuration disables reconciliation when the composition **doesn't** have `migration/resource-owner: "true"`.

### Step 3: Execute the Migration

Update the symphony to flip the annotation values, transferring ownership:

```yaml
spec:
  variations:
    - synthesizer:
        name: source-synth
      annotations:
        migration/resource-owner: ""  # Remove from source

    - synthesizer:
        name: target-synth
      annotations:
        migration/resource-owner: "true"  # Add to target
```

### Step 4: Clean up the Synths

Now that the migration is done, it's safe to remove the resource from the source synth.
Also remove the override from the target synth.

### Step 5: Clean up the Symphony

Remove the migration annotation from the symphony - you're done!
