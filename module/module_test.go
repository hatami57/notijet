package module

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/gormx/sqlite"
	"github.com/hatami57/microjet/host"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/hatami57/notijet/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlite.Driver().Open(gormx.Config{Name: ":memory:"}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return db
}

func newApp(t *testing.T, mods ...host.Module) *host.App {
	t.Helper()
	app, err := host.New()
	require.NoError(t, err)
	app.WithModules(mods...)
	return app
}

// start runs the app until the test ends.
func start(t *testing.T, app *host.App) {
	t.Helper()
	require.NoError(t, app.Start(context.Background()))
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

func sms() *notijet.Message { return &notijet.Message{To: []string{"0912"}, Text: "hi"} }

func TestNotifierOnly(t *testing.T) {
	a := memory.New(memory.WithName("a"))
	b := memory.New(memory.WithName("b"))
	var hooked int
	app := newApp(t, New(
		WithProvider("primary", a),
		WithProvider("backup", b),
		WithDefault("backup"),
		WithHooks(notijet.Hooks{AfterSend: func(context.Context, notijet.SendEvent) { hooked++ }}),
	))
	require.NoError(t, app.Err())
	start(t, app)

	n := Notifier(app)
	assert.Equal(t, []string{"primary", "backup"}, n.Names())
	res, err := n.Send(context.Background(), notijet.ChannelSMS, sms())
	require.NoError(t, err)
	assert.Equal(t, "b", res.Provider, "WithDefault applies")
	assert.Equal(t, 1, hooked)

	_, ok := LookupQueue(app)
	assert.False(t, ok, "no queue unless enabled")
	assert.Panics(t, func() { Queue(app) })
	assert.Equal(t, "notijet", New().ModuleName())
}

func TestQueueOnAppDatabase(t *testing.T) {
	sms1 := memory.New()
	app := newApp(t,
		gormx.Inject(openDB(t)),
		New(WithProvider("", sms1), WithQueue(queue.Interval(time.Hour))),
	)
	require.NoError(t, app.Err())

	q := Queue(app)
	_, err := q.Enqueue(context.Background(), notijet.ChannelSMS, sms())
	assert.ErrorContains(t, err, "not set up", "the store connects during setup")

	start(t, app)
	assert.True(t, gormx.Of(app).Migrator().HasTable("notijet_messages"))
	id, err := q.Enqueue(context.Background(), notijet.ChannelSMS, sms())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sms1.Len() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the worker delivers it")
	require.Eventually(t, func() bool {
		rec, _ := q.Get(context.Background(), id)
		return rec != nil && rec.Status == queue.StatusSent
	}, 5*time.Second, 10*time.Millisecond)
}

func TestQueueJoinsAppTransaction(t *testing.T) {
	db := openDB(t)
	app := newApp(t, gormx.Inject(db), New(WithProvider("", memory.New()), WithoutWorker()))
	start(t, app)
	repo := gormx.NewBaseRepository(gormx.Of(app))
	q := Queue(app)

	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		msg := sms()
		msg.ID = "with-order"
		_, err := q.Enqueue(ctx, notijet.ChannelSMS, msg)
		return err
	})
	require.NoError(t, err)
	rec, err := q.Get(context.Background(), "with-order")
	require.NoError(t, err)
	require.NotNil(t, rec)
	time.Sleep(50 * time.Millisecond)
	rec, _ = q.Get(context.Background(), "with-order")
	assert.Equal(t, queue.StatusQueued, rec.Status, "WithoutWorker leaves delivery to another process")
}

func TestCustomStoreAndNamedDatabase(t *testing.T) {
	db := openDB(t)
	store := queue.NewDBStore(db)
	require.NoError(t, store.Migrate())
	mem := memory.New()
	app := newApp(t, New(WithProvider("", mem), WithStore(store), WithQueue(queue.Interval(time.Hour))))
	start(t, app)
	_, err := Queue(app).Enqueue(context.Background(), notijet.ChannelSMS, sms())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return mem.Len() == 1 }, 5*time.Second, 10*time.Millisecond)

	named := newApp(t, gormx.Inject(openDB(t), "notify"), New(WithProvider("", memory.New()), WithDatabase("notify")))
	start(t, named)
	assert.True(t, gormx.Of(named, "notify").Migrator().HasTable("notijet_messages"))
}

func TestSetupErrors(t *testing.T) {
	app := newApp(t, New(WithProvider("", memory.New()), WithQueue()))
	err := app.Start(context.Background())
	require.Error(t, err, "the queue needs a database")
	assert.Contains(t, err.Error(), "gormx.Module")

	dup := newApp(t, New(WithProvider("x", memory.New()), WithProvider("x", memory.New())))
	assert.Error(t, dup.Err(), "duplicate provider names")
	missing := newApp(t, New(WithDefault("nope")))
	assert.ErrorIs(t, missing.Err(), notijet.ErrNoProvider)
}

func TestLazyStoreBeforeSetup(t *testing.T) {
	l := &lazyStore{}
	ctx := context.Background()
	_, err := l.Insert(ctx, &queue.Record{})
	assert.Error(t, err)
	_, err = l.Claim(ctx, time.Now(), time.Minute, 1)
	assert.Error(t, err)
	_, err = l.Finish(ctx, &queue.Record{})
	assert.Error(t, err)
	_, err = l.Get(ctx, "x")
	assert.Error(t, err)
	_, err = l.Requeue(ctx, "x", time.Now())
	assert.Error(t, err)
	_, err = l.Cancel(ctx, "x", time.Now())
	assert.Error(t, err)
	_, err = l.Prune(ctx, time.Now())
	assert.Error(t, err)

	db := openDB(t)
	inner := queue.NewDBStore(db)
	require.NoError(t, inner.Migrate())
	l.inner = inner
	ok, err := l.Requeue(ctx, "x", time.Now())
	assert.NoError(t, err)
	assert.False(t, ok)
	ok, err = l.Cancel(ctx, "x", time.Now())
	assert.NoError(t, err)
	assert.False(t, ok)
	n, err := l.Prune(ctx, time.Now())
	assert.NoError(t, err)
	assert.Zero(t, n)
}
