# Eno Reconciliation Variables Reference

## Overview

This document explains the four key variables used in Eno's reconciliation controller and how they relate to each other.

## Variable Comparison Table

| Variable | Type | Source | Represents | When it's `nil` | Key Characteristics |
|----------|------|--------|------------|----------------|---------------------|
| **`resource`** | `*resource.Resource` | Current synthesis tree (cache) | **Current desired state** from latest ResourceSlice | Never (exits early if not found) | • Raw manifest from synthesis<br>• Contains Eno annotations<br>• Has readiness checks, overrides<br>• Immutable representation |
| **`prev`** | `*resource.Resource` | Previous synthesis tree (cache) | **Previous desired state** from prior ResourceSlice | • No previous synthesis<br>• Resource newly added<br>• First synthesis | • Used to compute what changed<br>• Helps with field ownership<br>• Same structure as `resource` |
| **`current`** | `*unstructured.Unstructured` | Live Kubernetes cluster (API call) | **Actual live state** in the cluster right now | • Resource doesn't exist yet<br>• First time creating | • What actually exists<br>• Has real resourceVersion<br>• May have drift from desired |
| **`snap`** | `*resource.Snapshot` | Created from `resource.Snapshot(ctx, comp, current)` | **Reconciliation-ready version** of `resource` | Never | • Overrides applied<br>• Annotations evaluated into flags<br>• Eno metadata stripped<br>• Ready to send to cluster |
| **`parsed`** | `*unstructured.Unstructured` | Internal field in `Resource` and `Snapshot` | **Parsed K8s resource manifest** from ResourceSlice | Never (set during construction) | • Created from JSON in ResourceSlice<br>• One individual K8s resource<br>• Complete manifest (apiVersion, kind, metadata, spec)<br>• Stored in `Resource.parsed` |

## Lifecycle Flow

```
Synthesis → ResourceSlice → resource (desired) ──┐
                                                   ├─→ snap (ready to apply)
Live Cluster → current (actual) ─────────────────┘

Previous Synthesis → prev (old desired) → Used for diffing
```

## Detailed Explanations

### `resource` - Current Desired State
- **What it is**: The desired state from the current synthesis, stored in ResourceSlices
- **Where it comes from**: Built from ResourceSlice manifests and stored in the synthesis tree
- **Purpose**: Represents what Eno wants the resource to be
- **Contains**: Raw manifest JSON, readiness checks, override operations, metadata

### `prev` - Previous Desired State
- **What it is**: The desired state from the previous synthesis
- **Where it comes from**: Previous synthesis tree (if exists)
- **Purpose**: Understand what changed between synthesis runs
- **Used for**: 
  - Computing diffs for merge patches
  - Managing field ownership transitions
  - Ensuring Eno removes fields no longer in desired state

### `current` - Actual Cluster State
- **What it is**: The live resource fetched from the Kubernetes cluster
- **Where it comes from**: API call to upstream cluster via `getCurrent()`
- **Purpose**: Know what actually exists right now
- **Contains**: Real resourceVersion, any drift, actual field values

### `snap` - Reconciliation Snapshot
- **What it is**: A processed, ready-to-apply version of `resource`
- **Where it comes from**: Created by `resource.Snapshot(ctx, comp, current)`
- **Purpose**: The actual object that gets applied to the cluster
- **Processing includes**:
  - Applying overrides from patches
  - Evaluating Eno annotations (e.g., `eno.azure.io/disable-reconciliation`)
  - Setting runtime flags (Disable, DisableUpdates, Replace, Orphan, etc.)
  - Removing Eno metadata before applying

### `parsed` - Internal Parsed Manifest
- **What it is**: The parsed Kubernetes resource manifest (internal field in `Resource` and `Snapshot`)
- **Where it comes from**: Created in `FromSlice()` by unmarshaling the JSON manifest from ResourceSlice
- **Purpose**: Store the complete K8s resource as a Go object for manipulation
- **Key points**:
  - Created via `parsed.UnmarshalJSON([]byte(resource.Manifest))`
  - Represents ONE individual Kubernetes resource (ConfigMap, Deployment, etc.)
  - NOT the ResourceSlice itself - just one resource from within it
  - Stored as `Resource.parsed` and copied to `Snapshot.parsed` during snapshot creation
  - Deep copied when creating snapshots to prevent mutations

## Example Scenarios

### First Deployment (New Resource)
```
resource = ConfigMap with replicas: 3 (desired)
prev     = nil (no previous synthesis)
current  = nil (doesn't exist in cluster yet)
snap     = ConfigMap ready to create (Eno annotations removed)

Action: CREATE the resource
```

### Update After Synthesis
```
resource = ConfigMap with replicas: 5 (new desired state)
prev     = ConfigMap with replicas: 3 (old desired state)
current  = ConfigMap with replicas: 3 (what's in cluster)
snap     = ConfigMap with replicas: 5 (ready to apply update)

Action: PATCH/APPLY the update from 3→5 replicas
```

### Steady State (No Changes)
```
resource = ConfigMap with replicas: 5
prev     = ConfigMap with replicas: 5 (same as current)
current  = ConfigMap with replicas: 5 (matches desired)
snap     = ConfigMap with replicas: 5

Action: Nothing (already in sync)
```

### Resource with Eno Annotations
```yaml
# In ResourceSlice (resource):
apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    eno.azure.io/disable-updates: "true"
    app: myapp
data:
  key: value

# After Snapshot (snap):
apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    app: myapp  # Eno annotation removed
data:
  key: value

# snap.DisableUpdates = true (flag set from annotation)
```

## Quick Reference Guide

**When to use each variable:**

- **Need to know what to apply?** → Use `snap`
- **Need to know what's actually in the cluster?** → Use `current`
- **Need to know what changed between syntheses?** → Compare `prev` vs `resource`
- **Need the raw synthesized manifest?** → Use `resource`
- **Need the internal parsed K8s object?** → Access `resource.parsed` or `snap.parsed` (usually via `snap.Unstructured()`)

## Data Flow

```
ResourceSlice (JSON manifest string)
    ↓ UnmarshalJSON
parsed (unstructured.Unstructured) → stored in Resource.parsed
    ↓ Copy & modify
Snapshot.parsed (with overrides, Eno metadata removed)
    ↓ DeepCopy
snap.Unstructured() → Applied to cluster
```

## Related Concepts

### Synthesis Trees
- The cache stores multiple trees, one per synthesis UUID
- Both `CurrentSynthesis` and `PreviousSynthesis` trees are kept
- Trees are built during the reconstitution process (not during synthesis)
- Old trees are garbage collected when no longer referenced

### Resource vs Snapshot
- **Resource** = Blueprint (static desired state)
- **Snapshot** = Work order (dynamic, context-aware, ready to execute)

### Tree Visibility
Resources in the tree may not be "visible" (ready to reconcile) if:
- They have pending dependencies (other resources in earlier readiness groups)
- They would cause a backtrack (a later version is already ready)

## Code Location
- Main reconciliation logic: `internal/controllers/reconciliation/controller.go`
- Resource types: `internal/resource/resource.go`
- Tree structure: `internal/resource/tree.go`
- Cache management: `internal/resource/cache.go`
