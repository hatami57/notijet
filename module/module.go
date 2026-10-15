// Package module wires notijet into a microjet host app: a shared
// notijet.Notifier and, optionally, a persistent delivery queue on the app's
// gormx database with its background worker.
//
//	host.MustNew().WithModules(
//	    gormx.Module(postgres.Driver()),
//	    module.New(
//	        module.WithProvider("otp", notijet.Failover(kavenegarOTP, smsirOTP)),
//	        module.WithProvider("mail", smtp.Gmail.New(smtp.WithAuth(user, pass))),
//	        module.WithQueue(queue.MaxAttempts(8)),
//	    ),
//	).MustRun()
//
//	// anywhere with the app:
//	module.Notifier(app).Send(ctx, notijet.ChannelSMS, msg)          // now
//	module.Queue(app).Enqueue(ctx, notijet.ChannelEmail, msg)        // in the caller's transaction
//
// The Notifier logs through the app logger and the queue uses the app clock.
// The queue table (notijet_messages) is migrated during setup, after the
// database connects; the worker starts and stops with the app.
package module

import (
	"context"
	"time"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/host"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/queue"
)

// Name is the subject of the errors the package returns.
const Name = "notijet"

type provider struct {
	name string
	p    notijet.Provider
}

type config struct {
	providers []provider
	defaults  []string
	hooks     []notijet.Hooks

	queue     bool
	queueOpts []queue.Option
	database  string
	store     queue.Store
	worker    bool
}

// Option configures the module.
type Option func(*config)

// WithProvider registers p in the Notifier under name ("" means p.Name());
// the first provider of each channel is that channel's default.
func WithProvider(name string, p notijet.Provider) Option {
	return func(c *config) { c.providers = append(c.providers, provider{name, p}) }
}

// WithDefault makes the provider registered under name its channel's
// default.
func WithDefault(name string) Option {
	return func(c *config) { c.defaults = append(c.defaults, name) }
}

// WithHooks adds hooks run around every send through the Notifier.
func WithHooks(h notijet.Hooks) Option {
	return func(c *config) { c.hooks = append(c.hooks, h) }
}

// WithQueue enables the persistent queue on the app's default gormx database
// (see WithDatabase) and runs its worker, configured by opts.
func WithQueue(opts ...queue.Option) Option {
	return func(c *config) {
		c.queue = true
		c.queueOpts = append(c.queueOpts, opts...)
	}
}

// WithDatabase keeps the queue in the named gormx database. It enables the
// queue.
func WithDatabase(name string) Option {
	return func(c *config) { c.queue, c.database = true, name }
}

// WithStore keeps the queue in store instead of a gormx database. It enables
// the queue.
func WithStore(s queue.Store) Option {
	return func(c *config) { c.queue, c.store = true, s }
}

// WithoutWorker enables the queue for enqueueing only, for apps where another
// process runs the worker.
func WithoutWorker() Option {
	return func(c *config) { c.queue, c.worker = true, false }
}

// Module is the notijet host module.
type Module struct {
	cfg config
}

// New returns the module.
func New(opts ...Option) *Module {
	m := &Module{cfg: config{database: gormx.DefaultDatabase, worker: true}}
	for _, opt := range opts {
		opt(&m.cfg)
	}
	return m
}

// ModuleName implements host.NamedModule.
func (m *Module) ModuleName() string { return "notijet" }

// Register implements host.Module.
func (m *Module) Register(app *host.App) error {
	n := notijet.NewNotifier(notijet.WithLogger(app.Logger))
	for _, p := range m.cfg.providers {
		if err := n.Register(p.name, p.p); err != nil {
			return err
		}
	}
	for _, name := range m.cfg.defaults {
		if err := n.SetDefault(name); err != nil {
			return err
		}
	}
	for _, h := range m.cfg.hooks {
		n.AddHooks(h)
	}
	host.ProvideService(app, n)

	if !m.cfg.queue {
		return app.Err()
	}
	// The queue is registered now, so other services can resolve it during
	// their own Init; its store is connected in Setup, once the database is.
	store := &lazyStore{inner: m.cfg.store}
	opts := append([]queue.Option{queue.WithLogger(app.Logger), queue.WithClock(app.Clock.Now)}, m.cfg.queueOpts...)
	q := queue.New(store, n, opts...)
	host.ProvideService(app, q)
	host.ProvideService(app, &queueService{database: m.cfg.database, store: store})
	if m.cfg.worker {
		app.WithWorker("notijet-queue", func(ctx context.Context, _ *host.App) error {
			return q.Run(ctx)
		})
	}
	return app.Err()
}

// Notifier returns the app's Notifier, panicking when the module is not
// installed.
func Notifier(app *host.App) *notijet.Notifier {
	return host.MustResolveService[*notijet.Notifier](app)
}

// Queue returns the app's queue, panicking when the module is not installed
// with the queue enabled.
func Queue(app *host.App) *queue.Queue {
	return host.MustResolveService[*queue.Queue](app)
}

// LookupQueue returns the app's queue and whether there is one.
func LookupQueue(app *host.App) (*queue.Queue, bool) {
	return host.ResolveService[*queue.Queue](app)
}

// queueService connects the queue's store to the database during setup.
type queueService struct {
	database string
	store    *lazyStore
}

// Setup implements host.ServiceSetupper: it builds the database store and
// migrates its table, unless the app supplied its own store.
func (s *queueService) Setup(app *host.App) error {
	if s.store.inner != nil {
		return nil
	}
	db, ok := gormx.Lookup(app, s.database)
	if !ok {
		return errorx.NewInternalError(Name, "the queue needs a database; install gormx.Module first or use WithStore", "database", s.database)
	}
	store := queue.NewDBStore(db)
	if err := store.Migrate(); err != nil {
		return errorx.NewInternalError(Name, "migrating the queue table failed").WithInner(err)
	}
	s.store.inner = store
	return nil
}

// lazyStore is the queue's Store until setup connects the real one.
type lazyStore struct{ inner queue.Store }

func (l *lazyStore) get() (queue.Store, error) {
	if l.inner == nil {
		return nil, errorx.NewInternalError(Name, "the queue is not set up yet; use it after the app has started")
	}
	return l.inner, nil
}

func (l *lazyStore) Insert(ctx context.Context, rec *queue.Record) (bool, error) {
	s, err := l.get()
	if err != nil {
		return false, err
	}
	return s.Insert(ctx, rec)
}

func (l *lazyStore) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*queue.Record, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Claim(ctx, now, lease, limit)
}

func (l *lazyStore) Finish(ctx context.Context, rec *queue.Record) (bool, error) {
	s, err := l.get()
	if err != nil {
		return false, err
	}
	return s.Finish(ctx, rec)
}

func (l *lazyStore) Get(ctx context.Context, id string) (*queue.Record, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (l *lazyStore) Requeue(ctx context.Context, id string, now time.Time) (bool, error) {
	s, err := l.get()
	if err != nil {
		return false, err
	}
	return s.Requeue(ctx, id, now)
}

func (l *lazyStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	s, err := l.get()
	if err != nil {
		return 0, err
	}
	return s.Prune(ctx, before)
}
