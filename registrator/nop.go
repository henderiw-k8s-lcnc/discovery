package registrator

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newNopRegistrator(o *Options, opts ...Option) Registrator {
	return &nopRegistrator{
		address: o.Address,
	}
}

// nopRegistrator implements the Registrator interface
type nopRegistrator struct {
	address string
}

func (r *nopRegistrator) WithClient(c client.Client) {}

func (r *nopRegistrator) Init(ctx context.Context) {}

func (r *nopRegistrator) Register(ctx context.Context, s *Service) {}

func (r *nopRegistrator) DeRegister(ctx context.Context, id string) {}

func (r *nopRegistrator) Query(ctx context.Context, serviceName string, tags []string) ([]*Service, error) {
	return nil, nil
}

func (r *nopRegistrator) GetEndpointAddress(ctx context.Context, serviceName string, tags []string) (string, error) {
	return r.address, nil
}

func (r *nopRegistrator) Watch(ctx context.Context, serviceName string, tags []string, opts WatchOptions) chan *ServiceResponse {
	return nil
}

func (r *nopRegistrator) WatchCh(ctx context.Context, serviceName string, tags []string, opts WatchOptions, ch chan *ServiceResponse) {
}

func (r *nopRegistrator) StopWatch(serviceName string) {}
