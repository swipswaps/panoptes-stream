package etcd

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"sort"
	"strconv"
	"time"

	"go.etcd.io/etcd/clientv3"
	"go.etcd.io/etcd/clientv3/concurrency"
	"go.etcd.io/etcd/clientv3/yaml"
	"go.uber.org/zap"

	"git.vzbuilders.com/marshadrad/panoptes/config"
	"git.vzbuilders.com/marshadrad/panoptes/discovery"
)

type Etcd struct {
	id          string
	cfg         config.Config
	logger      *zap.Logger
	client      *clientv3.Client
	session     *concurrency.Session
	lockHandler *concurrency.Mutex
}

func New(cfg config.Config) (discovery.Discovery, error) {
	etcdConfig, err := yaml.NewConfig(cfg.Global().Discovery.ConfigFile)
	if err != nil {
		return nil, err
	}

	etcd := &Etcd{
		cfg:    cfg,
		logger: cfg.Logger(),
	}

	etcdConfig.TLS = nil
	etcd.client, err = clientv3.New(*etcdConfig)
	if err != nil {
		return nil, err
	}

	return etcd, nil
}

func (e *Etcd) Register() error {
	e.lock()
	defer e.unlock()

	meta := make(map[string]string)
	meta["shard_enabled"] = strconv.FormatBool(e.cfg.Global().Shard.Enabled)
	meta["shard_nodes"] = strconv.Itoa(e.cfg.Global().Shard.NumberOfNodes)
	meta["version"] = e.cfg.Global().Version

	ids := []int{}
	instances, err := e.GetInstances()
	if err != nil {
		return err
	}

	for _, instance := range instances {
		id, err := strconv.Atoi(instance.ID)
		if err != nil {
			e.logger.Warn("etcd.register", zap.Error(err))
			continue
		}
		ids = append(ids, id)

		if instance.Address == hostname() {
			if err := e.register(instance.ID, meta); err != nil {
				return err
			}

			e.logger.Info("consul service registery recovered", zap.String("id", instance.ID))

			e.id = instance.ID

			return nil
		}
	}

	e.id = getID(ids)
	e.register(e.id, meta)

	// TODO check lease id > 0

	go e.Watch(nil)

	return nil
}
func (e *Etcd) Deregister() error {
	return nil
}
func (e *Etcd) Watch(ch chan<- struct{}) {
	rch := e.client.Watch(context.Background(), e.cfg.Global().Discovery.Prefix, clientv3.WithPrefix())
	for wresp := range rch {
		for _, ev := range wresp.Events {
			e.logger.Info("etcd watcher triggered", zap.ByteString("key", ev.Kv.Key))
			select {
			case ch <- struct{}{}:
			default:
				e.logger.Info("etcd watcher response dropped")
			}
		}
	}
}

func (e *Etcd) hearthBeat(leaseID clientv3.LeaseID) {
	ch, err := e.client.KeepAlive(context.Background(), leaseID)
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			_, ok := <-ch
			if !ok {
				e.logger.Error("close channel")
				break
			}
		}
		// TODO etcd unreachable
	}()
}

func (e *Etcd) register(id string, meta map[string]string) error {
	reg := discovery.Instance{
		ID:      id,
		Meta:    meta,
		Address: hostname(),
		Status:  "passing",
	}

	e.id = id

	requestTimeout, _ := time.ParseDuration("5s")

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	resp, err := e.client.Grant(ctx, 60)
	cancel()
	if err != nil {
		return err
	}

	b, _ := json.Marshal(&reg)

	ctx, cancel = context.WithTimeout(context.Background(), requestTimeout)
	prefix := path.Join(e.cfg.Global().Discovery.Prefix, e.id)
	_, err = e.client.Put(ctx, prefix, string(b), clientv3.WithLease(resp.ID))
	cancel()
	if err != nil {
		return err
	}

	e.hearthBeat(resp.ID)

	return err
}

func (e *Etcd) GetInstances() ([]discovery.Instance, error) {
	requestTimeout, _ := time.ParseDuration("5s")

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	resp, err := e.client.Get(ctx, e.cfg.Global().Discovery.Prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		return nil, err
	}

	var instances []discovery.Instance
	for _, ev := range resp.Kvs {
		instance := discovery.Instance{}
		if err := json.Unmarshal(ev.Value, &instance); err != nil {
			return nil, err
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

func (e *Etcd) lock() error {
	var err error
	e.session, err = concurrency.NewSession(e.client)
	if err != nil {
		return err
	}

	requestTimeout, _ := time.ParseDuration("5s")
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)

	e.lockHandler = concurrency.NewMutex(e.session, "/panoptes_global_locki/")
	e.lockHandler.Lock(ctx)
	cancel()

	return nil
}

func (e *Etcd) unlock() error {
	defer e.session.Close()

	requestTimeout, _ := time.ParseDuration("5s")
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	return e.lockHandler.Unlock(ctx)
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}

	return name
}

func getID(ids []int) string {
	idStr := "0"

	if len(ids) < 1 {
		return idStr
	}

	sort.Ints(ids)
	for i, id := range ids {
		if i != id {
			idsStr := strconv.Itoa(i)
			return idsStr
		}
	}

	idStr = strconv.Itoa(len(ids))

	return idStr
}
