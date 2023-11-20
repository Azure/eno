package reconstitution

import (
	"time"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
)

// New creates a new Manager, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, writeBatchInterval time.Duration) (Manager, error) {
	m := &reconcilerManager{
		Manager: mgr,
	}
	m.writeBuffer = newWriteBuffer(mgr.GetClient(), writeBatchInterval, 1)
	mgr.Add(m.writeBuffer)

	var err error
	m.reconstituter, err = newReconstituter(mgr)
	if err != nil {
		return nil, err
	}

	return m, nil
}

type Manager interface {
	GetClient() Client
	Add(rec Reconciler) error
}

type reconcilerManager struct {
	ctrl.Manager
	*reconstituter
	*writeBuffer
}

func (m *reconcilerManager) GetClient() Client { return m }

func (m *reconcilerManager) Add(rec Reconciler) error {
	rateLimiter := workqueue.DefaultControllerRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: rec.Name(),
	})
	qp := &queueProcessor{
		Client:  m.Manager.GetClient(),
		Queue:   queue,
		Recon:   m.reconstituter,
		Handler: rec,
		Logger:  m.Manager.GetLogger().WithValues("controller", rec.Name()),
	}
	m.reconstituter.AddQueue(queue)
	return m.Manager.Add(qp)
}
