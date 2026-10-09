package plan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writePlan(t, validPlan+"\n  unknown: true\n")
	_, err := Load(path)
	require.ErrorContains(t, err, "field unknown not found")
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writePlan(t, validPlan)
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 4, loaded.Spec.Run.Concurrency)
	require.Equal(t, 3, loaded.Spec.Run.BatchSize)
	require.Equal(t, "create", loaded.Spec.Test.Phases[0].Resources[0].Operation)
}

func writePlan(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

const validPlan = `apiVersion: stress.eno.azure.io/v1alpha1
kind: StressTestPlan
metadata:
  name: test
spec:
  run:
    namespacePrefix: eno-stress
    namespaceCount: 3
    concurrency: 4
    timeout: 1m
  setup:
    resources:
      - name: composition
        scope: namespace
        forEachNamespace: true
        template:
          apiVersion: eno.azure.io/v1
          kind: Composition
          metadata:
            name: test
    readiness:
      - resource: composition
        condition:
          status: MissingInputs
  test:
    phases:
      - name: inputs
        resources:
          - name: input
            scope: namespace
            forEachNamespace: true
            template:
              apiVersion: v1
              kind: ConfigMap
              metadata:
                name: input
  output:
    kind: ConfigMap
    name: output
    expectedData:
      value: ok`
