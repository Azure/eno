package framework

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func init() {
	err := apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	if err != nil {
		panic(fmt.Sprintf("failed to add eno scheme: %v", err))
	}
}

// NewClient creates a controller-runtime client using the in-cluster or KUBECONFIG config.
func NewClient(t *testing.T) client.Client {
	t.Helper()
	cfg, err := ctrl.GetConfig()
	require.NoError(t, err, "failed to get kubeconfig — is KUBECONFIG set?")

	cli, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err, "failed to create client")
	return cli
}

// UniqueName generates a test-unique resource name with a timestamp suffix.
func UniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}
