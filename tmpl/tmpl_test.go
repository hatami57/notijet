package tmpl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var files = fstest.MapFS{
	"templates/welcome.subject":    {Data: []byte("Welcome,\n {{.Name}}!")},
	"templates/welcome.txt":        {Data: []byte("\nHi {{.Name}}.\n{{template \"_footer.txt\" .}}\n")},
	"templates/welcome.html":       {Data: []byte(`<p>Hi {{.Name}}</p>{{template "_footer.html" .}}`)},
	"templates/welcome.fa.subject": {Data: []byte("خوش آمدید {{.Name}}")},
	"templates/welcome.fa.txt":     {Data: []byte("سلام {{.Name}}")},
	"templates/_footer.txt":        {Data: []byte("-- Acme")},
	"templates/_footer.html":       {Data: []byte(`<footer>Acme</footer>`)},
	"templates/orders/shipped.txt": {Data: []byte("Order {{.ID | upper}} shipped")},
	"templates/otp.txt":            {Data: []byte("Your code is {{.code}}")},
	"templates/README.md":          {Data: []byte("ignored")},
	"other/stray.txt":              {Data: []byte("outside the dir")},
}

func parse(t *testing.T, opts ...Option) *Set {
	t.Helper()
	s, err := ParseFS(files, "templates", append([]Option{WithFuncs(map[string]any{"upper": strings.ToUpper})}, opts...)...)
	require.NoError(t, err)
	return s
}

func TestRender(t *testing.T) {
	s := parse(t)
	r, err := s.Render("welcome", map[string]any{"Name": "Ali"})
	require.NoError(t, err)
	assert.Equal(t, "Welcome, Ali!", r.Subject, "a subject is collapsed to one line")
	assert.Equal(t, "Hi Ali.\n-- Acme", r.Text, "partials are included and whitespace trimmed")
	assert.Equal(t, "<p>Hi Ali</p><footer>Acme</footer>", r.HTML)

	r, err = s.Render("welcome", map[string]any{"Name": "<script>"})
	require.NoError(t, err)
	assert.Equal(t, "<p>Hi &lt;script&gt;</p><footer>Acme</footer>", r.HTML, "HTML is escaped")
	assert.Equal(t, "Hi <script>.\n-- Acme", r.Text, "text is not")

	r, err = s.Render("orders/shipped", map[string]any{"ID": "ab12"})
	require.NoError(t, err)
	assert.Equal(t, "Order AB12 shipped", r.Text)
	assert.Empty(t, r.Subject)
	assert.Empty(t, r.HTML)
}

func TestLocales(t *testing.T) {
	s := parse(t)
	r, err := s.RenderLocale("welcome", "fa-IR", map[string]any{"Name": "علی"})
	require.NoError(t, err)
	assert.Equal(t, "خوش آمدید علی", r.Subject)
	assert.Equal(t, "سلام علی", r.Text)
	assert.Empty(t, r.HTML, "the fa variant has no HTML; parts are never mixed across languages")

	r, err = s.RenderLocale("welcome", "de", map[string]any{"Name": "Ali"})
	require.NoError(t, err)
	assert.Equal(t, "Welcome, Ali!", r.Subject, "unknown locales fall back to the base variant")

	assert.Equal(t, []string{"a.fa-IR-x", "a.fa-IR", "a.fa", "a"}, candidates("a", "fa-IR-x"))
	assert.Equal(t, []string{"a.en_US", "a.en", "a"}, candidates("a", "en_US"))
	assert.Equal(t, []string{"a"}, candidates("a", " "))
}

func TestErrors(t *testing.T) {
	s := parse(t)
	_, err := s.Render("missing", nil)
	assert.ErrorIs(t, err, notijet.ErrTemplate)
	assert.ErrorContains(t, err, "no template named missing")

	_, err = s.Render("welcome", map[string]any{})
	assert.ErrorIs(t, err, notijet.ErrTemplate, "missing keys fail by default")
	assert.False(t, notijet.IsRetryable(err))

	_, err = s.Render("_footer", nil)
	assert.Error(t, err, "partials are not templates")

	lenient := parse(t, MissingKeyZero())
	r, err := lenient.Render("otp", map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "Your code is <no value>", r.Text, "text/template's zero for an interface map")
	r, err = lenient.Render("otp", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "Your code is", r.Text)

	require.Error(t, s.Add("late", Text, "x"), "adding after rendering")
	assert.Error(t, New().Add("", Text, "x"))
	assert.Error(t, New().Add("x", "md", "x"))
	assert.ErrorContains(t, New().Add("broken", Text, "{{.Name"), "parsing broken.txt")
	assert.ErrorContains(t, New().Add("broken", HTML, "{{end}}"), "parsing broken.html")

	_, err = ParseFS(fstest.MapFS{"bad.txt": {Data: []byte("{{")}}, "")
	assert.ErrorIs(t, err, notijet.ErrTemplate)
	_, err = ParseFS(files, "nope")
	assert.Error(t, err)
}

func TestAddAndRootDir(t *testing.T) {
	s := New()
	require.NoError(t, s.Add("ping", Subject, "Ping {{.N}}"))
	require.NoError(t, s.Add("ping", HTML, "<b>{{.N}}</b>"))
	assert.True(t, s.Has("ping", ""))
	assert.False(t, s.Has("pong", "fa"))
	r, err := s.Render("ping", map[string]int{"N": 3})
	require.NoError(t, err)
	assert.Equal(t, &Rendered{Subject: "Ping 3", HTML: "<b>3</b>"}, r)

	root, err := ParseFS(fstest.MapFS{"hello.txt": {Data: []byte("hi")}}, "")
	require.NoError(t, err)
	assert.True(t, root.Has("hello", ""))
}

func TestApply(t *testing.T) {
	s := parse(t)
	msg := &notijet.Message{To: []string{"a@x.io"}, HTML: "<p>keep</p>", Subject: "replace me"}
	require.NoError(t, s.Apply(msg, "welcome", "fa", map[string]any{"Name": "علی"}))
	assert.Equal(t, "خوش آمدید علی", msg.Subject)
	assert.Equal(t, "سلام علی", msg.Text)
	assert.Equal(t, "<p>keep</p>", msg.HTML, "parts the template lacks are left alone")
	assert.Error(t, s.Apply(nil, "welcome", "", nil))
	assert.Error(t, s.Apply(msg, "missing", "", nil))
}

func TestDecorate(t *testing.T) {
	s := parse(t)
	mem := memory.New(memory.WithChannel(notijet.ChannelEmail))
	p := Decorate(mem, s, func(m *notijet.Message) string { return m.Metadata["locale"] })
	assert.Equal(t, "memory", p.Name())

	orig := &notijet.Message{
		To:       []string{"a@x.io"},
		Template: notijet.NewTemplate("welcome", "Name", "Ali"),
		Metadata: map[string]string{"locale": "en"},
	}
	_, err := p.Send(context.Background(), orig)
	require.NoError(t, err)
	sent := mem.Last()
	assert.Nil(t, sent.Template, "rendered locally")
	assert.Equal(t, "Welcome, Ali!", sent.Subject)
	assert.Equal(t, "<p>Hi Ali</p><footer>Acme</footer>", sent.HTML)
	assert.NotNil(t, orig.Template, "the caller's message is untouched")
	assert.Empty(t, orig.Subject)

	// A template the set does not know is left for the provider.
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Template: notijet.NewTemplate("d-123")})
	require.NoError(t, err)
	assert.Equal(t, "d-123", mem.Last().Template.ID)

	// No template: unchanged.
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	require.NoError(t, err)

	// A render failure is reported, nothing sent.
	n := mem.Len()
	_, err = Decorate(mem, s, nil).Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Template: notijet.NewTemplate("welcome")})
	assert.ErrorIs(t, err, notijet.ErrTemplate)
	assert.Equal(t, n, mem.Len())

	_, err = p.Send(context.Background(), nil)
	assert.Error(t, err)
}

func TestConcurrentRender(t *testing.T) {
	s := parse(t)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			r, err := s.Render("welcome", map[string]any{"Name": i})
			if err == nil && !strings.Contains(r.HTML, "Hi ") {
				err = errors.New("bad render")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}
