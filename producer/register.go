package producer

import (
	"sync"

	"go.uber.org/zap"
)

type Registrar struct {
	p  map[string]ProducerFactory
	lg *zap.Logger
	sync.RWMutex
}

func NewRegistrar(lg *zap.Logger) *Registrar {
	return &Registrar{
		p:  make(map[string]ProducerFactory),
		lg: lg,
	}
}

func (pr *Registrar) Register(name, vendor string, pf ProducerFactory) {
	pr.lg.Info("producer/register", zap.String("name", name), zap.String("vendor", vendor))
	pr.set(name, pf)
}

func (pr *Registrar) GetProducerFactory(name string) (ProducerFactory, bool) {
	return pr.get(name)
}

func (pr *Registrar) set(name string, m ProducerFactory) {
	pr.Lock()
	defer pr.Unlock()
	pr.p[name] = m
}

func (pr *Registrar) get(name string) (ProducerFactory, bool) {
	pr.RLock()
	defer pr.RUnlock()
	v, ok := pr.p[name]

	return v, ok
}