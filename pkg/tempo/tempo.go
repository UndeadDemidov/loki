package tempo

import (
	"flag"
	"fmt"

	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/cortexproject/cortex/pkg/ring"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/weaveworks/common/middleware"
	"github.com/weaveworks/common/server"

	"github.com/grafana/tempo/pkg/distributor"
	"github.com/grafana/tempo/pkg/ingester"
	"github.com/grafana/tempo/pkg/ingester/client"
	"github.com/grafana/tempo/pkg/querier"
)

// Config is the root config for Tempo.
type Config struct {
	Target      moduleName `yaml:"target,omitempty"`
	AuthEnabled bool       `yaml:"auth_enabled,omitempty"`

	Server         server.Config      `yaml:"server,omitempty"`
	Distributor    distributor.Config `yaml:"distributor,omitempty"`
	Querier        querier.Config     `yaml:"querier,omitempty"`
	IngesterClient client.Config      `yaml:"ingester_client,omitempty"`
	Ingester       ingester.Config    `yaml:"ingester,omitempty"`
}

// RegisterFlags registers flag.
func (c *Config) RegisterFlags(f *flag.FlagSet) {
	c.Server.MetricsNamespace = "tempo"
	c.Target = All
	f.Var(&c.Target, "target", "target module (default All)")
	f.BoolVar(&c.AuthEnabled, "auth.enabled", true, "Set to false to disable auth.")

	c.Server.RegisterFlags(f)
	c.Distributor.RegisterFlags(f)
	c.Querier.RegisterFlags(f)
	c.IngesterClient.RegisterFlags(f)
	c.Ingester.RegisterFlags(f)
}

// Tempo is the root datastructure for Tempo.
type Tempo struct {
	cfg Config

	server      *server.Server
	ring        *ring.Ring
	distributor *distributor.Distributor
	ingester    *ingester.Ingester
	querier     *querier.Querier

	httpAuthMiddleware middleware.Interface

	inited map[moduleName]struct{}
}

// New makes a new Tempo.
func New(cfg Config) (*Tempo, error) {
	tempo := &Tempo{
		cfg:    cfg,
		inited: map[moduleName]struct{}{},
	}

	tempo.setupAuthMiddleware()

	if err := tempo.init(cfg.Target); err != nil {
		return nil, err
	}

	return tempo, nil
}

func (t *Tempo) setupAuthMiddleware() {
	if t.cfg.AuthEnabled {
		t.cfg.Server.GRPCMiddleware = []grpc.UnaryServerInterceptor{
			middleware.ServerUserHeaderInterceptor,
		}
		t.cfg.Server.GRPCStreamMiddleware = []grpc.StreamServerInterceptor{
			middleware.StreamServerUserHeaderInterceptor,
		}
		t.httpAuthMiddleware = middleware.AuthenticateUser
	} else {
		t.cfg.Server.GRPCMiddleware = []grpc.UnaryServerInterceptor{
			fakeGRPCAuthUniaryMiddleware,
		}
		t.cfg.Server.GRPCStreamMiddleware = []grpc.StreamServerInterceptor{
			fakeGRPCAuthStreamMiddleware,
		}
		t.httpAuthMiddleware = fakeHTTPAuthMiddleware
	}
}

func (t *Tempo) init(m moduleName) error {
	if _, ok := t.inited[m]; ok {
		return nil
	}

	for _, dep := range modules[m].deps {
		if err := t.init(dep); err != nil {
			return err
		}
	}

	level.Info(util.Logger).Log("msg", "initialising", "module", m)
	if modules[m].init != nil {
		if err := modules[m].init(t); err != nil {
			return errors.Wrap(err, fmt.Sprintf("error initialising module: %s", m))
		}
	}

	t.inited[m] = struct{}{}
	return nil
}

// Run starts Tempo running, and blocks until a signal is received.
func (t *Tempo) Run() error {
	return t.server.Run()
}

// Stop gracefully stops a Tempo.
func (t *Tempo) Stop() error {
	t.server.Shutdown()
	t.stop(t.cfg.Target)
	return nil
}

func (t *Tempo) stop(m moduleName) {
	if _, ok := t.inited[m]; !ok {
		return
	}
	delete(t.inited, m)

	for _, dep := range modules[m].deps {
		t.stop(dep)
	}

	if modules[m].stop == nil {
		return
	}

	level.Info(util.Logger).Log("msg", "stopping", "module", m)
	if err := modules[m].stop(t); err != nil {
		level.Error(util.Logger).Log("msg", "error stopping", "module", m, "err", err)
	}
}
