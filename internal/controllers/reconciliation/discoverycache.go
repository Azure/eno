package reconciliation

import (
	"sync"

	"k8s.io/kubectl/pkg/util/openapi"
)

// TODO

type discoveryCache struct {
	mut     sync.Mutex
	current openapi.Resources
}

func (d *discoveryCache) Get() openapi.Resources {
	return nil
}
