package registrator

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultConsulPort   = "8500"
	defaultWatchTimeout = 1 * time.Minute
)

type consulConfig struct {
	Namespace  string // namespace in which consul is deployed
	Address    string // address of the consul client
	Datacenter string // default kind-dc1
	Username   string
	Password   string
	Token      string
}

// consul implements the Registrator interface
type consul struct {
	//serviceConfig *serviceConfig
	consulConfig *consulConfig
	// kubernetes
	client client.Client
	// consul
	consulClient *api.Client
	// services
	m        sync.Mutex
	services map[string]chan struct{}      // used to stop the registration
	cfn      map[string]context.CancelFunc // used for canceling the watch
}

func newConsulRegistrator(ctx context.Context, c client.Client, namespace, dcName string, opts ...Option) (Registrator, error) {
	// if the namespace is not provided we initialize to consul namespace
	if namespace == "" {
		namespace = "consul"
	}

	r := &consul{
		consulConfig: &consulConfig{
			Namespace:  namespace,
			Datacenter: dcName,
		},
		client:   c,
		services: map[string]chan struct{}{},
		cfn:      map[string]context.CancelFunc{},
	}

	for _, opt := range opts {
		opt(r)
	}

	r.init(ctx)

	if err := r.createClient(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *consul) createClient() error {
	//log := r.log.WithValues("Consul", *r.consulConfig)
	//	log.Debug("consul create client...")

	clientConfig := &api.Config{
		Address:    r.consulConfig.Address,
		Scheme:     "http",
		Datacenter: r.consulConfig.Datacenter,
		Token:      r.consulConfig.Token,
	}
	if r.consulConfig.Username != "" && r.consulConfig.Password != "" {
		clientConfig.HttpAuth = &api.HttpBasicAuth{
			Username: r.consulConfig.Username,
			Password: r.consulConfig.Password,
		}
	}

	var err error
	if r.consulClient, err = api.NewClient(clientConfig); err != nil {
		//log.Debug("failed to connect to consul", "error", err)
		return err
	}
	_, err = r.consulClient.Agent().Self()
	if err != nil {
		//log.Debug("failed to connect to consul", "error", err)
		time.Sleep(1 * time.Second)
		return err
	}
	/*
		if cfg, ok := self["Config"]; ok {
			b, _ := json.Marshal(cfg)
			//log.Debug("consul agent config:", "agent config", string(b))
		}
	*/
	return nil
}

func (r *consul) WithClient(c client.Client) {
	r.client = c
}

func (r *consul) init(ctx context.Context) {
	//log := r.log.WithValues("Consul", *r.consulConfig)
	//log.Debug("consul init, trying to find daemonset...")

CONSULDAEMONSETPOD:
	// get all the pods in the consul namespace
	opts := []client.ListOption{
		client.InNamespace(r.consulConfig.Namespace),
	}
	pods := &corev1.PodList{}
	if err := r.client.List(ctx, pods, opts...); err != nil {
		//log.Debug("cannot list pods on k8s api", "err", err)
		time.Sleep(2 * time.Second)
		goto CONSULDAEMONSETPOD
	}

	found := false
	for _, pod := range pods.Items {
		/*
			log.Debug("consul pod",
				"consul pod kind", pod.OwnerReferences[0].Kind,
				"consul pod phase", pod.Status.Phase,
				"consul pod node name", pod.Spec.NodeName,
				"consul pod node ip", pod.Status.HostIP,
				"consul pod ip", pod.Status.PodIP,
				"pod node naame", os.Getenv("NODE_NAME"),
				"pod node ip", os.Getenv("Node_IP"),
			)
		*/
		if len(pod.OwnerReferences) == 0 {
			// pod has no owner
			continue
		}
		switch pod.OwnerReferences[0].Kind {
		case "DaemonSet":
			if pod.Status.Phase == "Running" &&
				pod.Status.PodIP != "" &&
				pod.Spec.NodeName == os.Getenv("NODE_NAME") {
				//pod.Status.HostIP == os.Getenv("Node_IP") {
				found = true
				r.consulConfig.Address = strings.Join([]string{pod.Status.PodIP, defaultConsulPort}, ":") // TODO
				//r.consulConfig.Datacenter = defaultDCName
			}
		default:
			// could be ReplicaSet, StatefulSet, etc, but not releant here
			continue
		}
	}
	if !found {
		// daemonset not found
		//log.Debug("consul daemonset not found")
		time.Sleep(defaultWaitTime)
		goto CONSULDAEMONSETPOD
	}
	//log.Debug("consul daemonset found", "address", r.consulConfig.Address, "datacenter", r.consulConfig.Datacenter)
}

func (r *consul) Register(ctx context.Context, s *Service) {
	r.m.Lock()
	defer r.m.Unlock()
	r.services[s.ID] = make(chan struct{})
	go r.registerService(ctx, s, r.services[s.ID])
}

func (r *consul) DeRegister(ctx context.Context, id string) {
	//log := r.log.WithValues("Consul", r.consulConfig)
	//log.Debug("Deregister...")

	r.m.Lock()
	defer r.m.Unlock()
	if stopCh, ok := r.services[id]; ok {
		close(stopCh)
		delete(r.services, id)
	}
}

func (r *consul) registerService(ctx context.Context, s *Service, stopCh chan struct{}) {
	//log := r.log.WithValues("Consul", r.consulConfig)
	//log.Debug("Register...")
INITCONSUL:
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	service := &api.AgentServiceRegistration{
		ID:      s.ID,
		Name:    s.Name,
		Address: s.Address,
		Port:    s.Port,
		Tags:    s.Tags,
	}
	ttlCheckID := ""
	for i, hc := range s.HealthChecks {
		switch hc {
		case HealthKindTTL:
			service.Checks = append(service.Checks,
				&api.AgentServiceCheck{
					TTL:                            defaultRegistrationCheckInterval.String(),
					DeregisterCriticalServiceAfter: (defaultMaxServiceFail * defaultRegistrationCheckInterval).String(),
				})

			ttlCheckID = fmt.Sprintf("service:%s", s.ID)
			if len(s.HealthChecks) > 1 {
				ttlCheckID += fmt.Sprintf(":%d", i+1)
			}
		case HealthKindGRPC:
			service.Checks = append(service.Checks, &api.AgentServiceCheck{
				GRPC:                           s.Address + ":" + strconv.Itoa(s.Port),
				GRPCUseTLS:                     true,
				Interval:                       defaultRegistrationCheckInterval.String(),
				TLSSkipVerify:                  true,
				DeregisterCriticalServiceAfter: (defaultMaxServiceFail * defaultRegistrationCheckInterval).String(),
			})
		}
	}

	//b, _ := json.Marshal(service)
	//log.Debug("consul register service", "service", string(b))

	if err := r.consulClient.Agent().ServiceRegister(service); err != nil {
		//log.Debug("consul register service failed", "error", err)
		time.Sleep(defaultWaitTime)
		goto INITCONSUL
	}
	if ttlCheckID == "" {
		return
	}

	ticker := time.NewTicker(defaultRegistrationCheckInterval / 2)
	defer ticker.Stop()
	if err := r.consulClient.Agent().UpdateTTL(ttlCheckID, "", api.HealthPassing); err != nil {
		//log.Debug("consul failed to pass TTL check", "error", err)
	}
	for {
		select {
		case <-ticker.C:
			if err := r.consulClient.Agent().UpdateTTL(ttlCheckID, "", api.HealthPassing); err != nil {
				//log.Debug("consul failed to pass TTL check", "error", err)
			}
		case <-ctx.Done():
			r.consulClient.Agent().UpdateTTL(ttlCheckID, ctx.Err().Error(), api.HealthCritical)
			goto INITCONSUL
		case <-stopCh:
			//r.log.Debug("deregistering", "service", s.Name, "instance", s.ID)
			r.consulClient.Agent().ServiceDeregister(s.ID)
			return
		}
	}
}

// servicename : controllername + worker
func (r *consul) Query(ctx context.Context, serviceName string, tags []string) ([]*Service, error) {
	se, _, err := r.consulClient.Health().ServiceMultipleTags(serviceName, tags, true, &api.QueryOptions{})
	if err != nil {
		return nil, err
	}
	servcies := []*Service{}
	for _, srv := range se {
		addr := srv.Service.Address
		if addr == "" {
			addr = srv.Node.Address
		}
		servcies = append(servcies, &Service{
			ID: srv.Service.ID,
			//Address: net.JoinHostPort(addr, strconv.Itoa(srv.Service.Port)),
			Address: addr,
			Port:    srv.Service.Port,
			Tags:    srv.Service.Tags,
		})
	}
	return servcies, nil
}

func (r *consul) GetEndpointAddress(ctx context.Context, serviceName string, tags []string) (string, error) {
	svcs, err := r.Query(ctx, serviceName, tags)
	if err != nil {
		return "", errors.Wrap(err, "cannot get query from registrator")
	}
	if len(svcs) == 0 {
		return "", errors.New("target not available")
	}
	return fmt.Sprintf("%s:%s", svcs[0].Address, strconv.Itoa(svcs[0].Port)), nil
}

func (r *consul) Watch(ctx context.Context, serviceName string, tags []string, opts WatchOptions) chan *ServiceResponse {
	ch := make(chan *ServiceResponse)
	go r.WatchCh(ctx, serviceName, tags, opts, ch)
	return ch
}

func (r *consul) WatchCh(ctx context.Context, serviceName string, tags []string, opts WatchOptions, ch chan *ServiceResponse) {
	//log := r.log.WithValues("serviceName", serviceName)
	watchTimeout := defaultWatchTimeout
	cfn, ok := r.cfn[serviceName]
	if ok {
		cfn()
	}
	ctx, r.cfn[serviceName] = context.WithCancel(ctx)

	var index uint64
	qOpts := &api.QueryOptions{
		WaitIndex: index,
		WaitTime:  watchTimeout,
	}
	var err error
	// long blocking watch
	for {
		select {
		case <-ctx.Done():
			ch <- &ServiceResponse{
				ServiceName: serviceName,
				Err:         ctx.Err(),
			}
			return
		default:
			//log.Debug("(re)starting watch", "index", qOpts.WaitIndex)

			index, err = r.watch(qOpts.WithContext(ctx), serviceName, tags, ch)
			if err != nil {
				//log.Debug("watch error", "error", err)
			}
			if index == 0 {
				//log.Debug("index 0", "local index", qOpts.WaitIndex, "server index", index)
				// this is considered a bug in consul
				qOpts.WaitIndex = 1
				time.Sleep(2 * time.Second)
				continue
			}
			// expected sunce waitindex is bigger than the previous value
			if index > qOpts.WaitIndex {
				//log.Debug("index > qOpts.WaitIndex", "local index", qOpts.WaitIndex, "server index", index)
				qOpts.WaitIndex = index
			}
			// reset WaitIndex if the returned index decreases because a waitIndex should always increase
			// https://www.consul.io/api-docs/features/blocking#implementation-details
			if index < qOpts.WaitIndex {
				// restart to get back in sync
				//log.Debug("index < qOpts.WaitIndex -> restart", "local index", qOpts.WaitIndex, "server index", index)
				qOpts.WaitIndex = 0
			}
		}
	}
}

func (r *consul) watch(qOpts *api.QueryOptions, serviceName string, tags []string, sChan chan<- *ServiceResponse) (uint64, error) {
	//log := r.log.WithValues("serviceName", serviceName)
	se, meta, err := r.consulClient.Health().ServiceMultipleTags(serviceName, tags, true, qOpts)
	if err != nil {
		return 0, err
	}
	if meta == nil {
		meta = new(api.QueryMeta)
	}
	if meta.LastIndex == qOpts.WaitIndex {
		//log.Debug("service did not change", "index", meta.LastIndex)
		return meta.LastIndex, nil
	}
	if err != nil {
		return meta.LastIndex, err
	}
	if len(se) == 0 {
		return 1, nil
	}
	newSrvs := make([]*Service, 0)
	for _, srv := range se {
		addr := srv.Service.Address
		if addr == "" {
			addr = srv.Node.Address
		}
		newSrvs = append(newSrvs, &Service{
			ID: srv.Service.ID,
			//Address: net.JoinHostPort(addr, strconv.Itoa(srv.Service.Port)),
			Address: addr,
			Port:    srv.Service.Port,
			Tags:    srv.Service.Tags,
		})
	}
	sChan <- &ServiceResponse{
		ServiceName:      serviceName,
		ServiceInstances: newSrvs,
		Err:              nil,
	}
	return meta.LastIndex, nil
}

func (r *consul) StopWatch(serviceName string) {
	r.m.Lock()
	defer r.m.Unlock()
	if serviceName == "" {
		for _, cfn := range r.cfn {
			cfn()
		}
		return
	}
	if cfn, ok := r.cfn[serviceName]; ok {
		cfn()
	}
}
