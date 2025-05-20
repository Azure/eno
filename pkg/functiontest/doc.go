// Package functiontest provides testing utilities for Eno synthesizer functions.
//
// It provides a simple way to test synthesizer functions by running them with different inputs
// and asserting on the outputs. It also supports snapshot testing to validate that the outputs
// of a function match expected values.
//
// # Basic Usage
//
// A basic test for a function that creates a Pod:
//
//	func TestMyFunction(t *testing.T) {
//	    fn := func(inputs struct{}) ([]client.Object, error) {
//	        pod := &corev1.Pod{}
//	        pod.Name = "test-pod"
//	        return []client.Object{pod}, nil
//	    }
//
//	    functiontest.Evaluate(t, fn, functiontest.Scenario[struct{}]{
//	        Name:   "creates-pod",
//	        Inputs: struct{}{},
//	        Assertion: func(t *testing.T, scenario *functiontest.Scenario[struct{}], outputs []client.Object) {
//	            require.Len(t, outputs, 1)
//	            pod, ok := outputs[0].(*corev1.Pod)
//	            require.True(t, ok)
//	            assert.Equal(t, "test-pod", pod.Name)
//	        },
//	    })
//	}
//
// # Snapshot Testing
//
// To use snapshot testing:
//
//	func TestMyFunctionSnapshots(t *testing.T) {
//	    fn := func(inputs struct{}) ([]client.Object, error) {
//	        // Your function implementation
//	    }
//
//	    assertion := functiontest.LoadSnapshots[struct{}](t, "testdata/snapshots")
//	    scenarios := functiontest.LoadScenarios(t, "testdata/fixtures", assertion)
//	    functiontest.Evaluate(t, fn, scenarios...)
//	}
//
// To generate snapshots, create empty files in the snapshots directory and run the tests
// with the ENO_GEN_SNAPSHOTS environment variable set.
package functiontest