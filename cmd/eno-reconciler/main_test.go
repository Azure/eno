package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTombstoneRecoveryOptions(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		enabled              bool
		podNamespace         string
		compositionNamespace string
		wantNamespace        string
		wantError            string
	}{
		{name: "disabled without namespace"},
		{name: "disabled ignores namespace mismatch", podNamespace: "recovery", compositionNamespace: "other"},
		{name: "enabled without pod namespace", enabled: true, wantError: "POD_NAMESPACE is required when --enable-tombstone-recovery is enabled"},
		{name: "enabled watches all namespaces", enabled: true, podNamespace: "recovery", wantError: `--composition-namespace must match POD_NAMESPACE when tombstone recovery is enabled: got "", want "recovery"`},
		{name: "enabled namespace mismatch", enabled: true, podNamespace: "recovery", compositionNamespace: "other", wantError: `--composition-namespace must match POD_NAMESPACE when tombstone recovery is enabled: got "other", want "recovery"`},
		{name: "enabled matching namespace", enabled: true, podNamespace: "recovery", compositionNamespace: "recovery", wantNamespace: "recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", tc.podNamespace)
			opts, err := validateTombstoneRecoveryOptions(tc.enabled, tc.compositionNamespace)
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.enabled, opts.Enabled)
			assert.Equal(t, tc.wantNamespace, opts.Namespace)
		})
	}
}
