// Package tmpl renders notification content from local templates — a
// provider-neutral alternative to templates stored at a provider.
//
// A Set holds named templates, each with up to three parts, conventionally
// kept as files:
//
//	welcome.subject   subject line   (text/template)
//	welcome.txt       plain text     (text/template)
//	welcome.html      HTML body      (html/template, contextually escaped)
//	welcome.fa.txt    a locale variant, chosen by RenderLocale("welcome", "fa", ...)
//	_footer.html      a partial: files starting with "_" are not templates
//	                  themselves; use them with {{template "_footer.html" .}}
//
// Load them from any fs.FS, typically an embed.FS:
//
//	//go:embed templates
//	var files embed.FS
//
//	set, err := tmpl.ParseFS(files, "templates")
//	r, err := set.Render("welcome", map[string]any{"Name": "Ali"})
//	err = set.Apply(msg, "welcome", "", data)       // fills msg.Subject/Text/HTML
//	p := tmpl.Decorate(smtpProvider, set, nil)      // renders msg.Template locally
//
// A reference to a missing key is an error by default (see MissingKeyZero),
// so a typo in a template fails loudly instead of sending a blank.
package tmpl

import (
	"bytes"
	"context"
	"errors"
	htmltemplate "html/template"
	"io/fs"
	"path"
	"strings"
	"sync"
	texttemplate "text/template"

	"github.com/hatami57/notijet"
)

// Name is the subject of the errors the package returns.
const Name = "tmpl"

// Part is one part of a template.
type Part string

// The parts a template may have, named by their file extension.
const (
	Subject Part = "subject"
	Text    Part = "txt"
	HTML    Part = "html"
)

// Rendered is a rendered template. Parts the template does not define are
// empty.
type Rendered struct {
	Subject string
	Text    string
	HTML    string
}

// Set is a collection of templates. Build it fully — ParseFS, Add — before
// rendering: once a template has rendered, adding more is an error. Rendering
// is safe for concurrent use.
type Set struct {
	mu       sync.Mutex
	text     *texttemplate.Template
	html     *htmltemplate.Template
	parts    map[string]map[Part]bool // template stem → parts it has
	rendered bool
}

// Option configures a Set.
type Option func(*config)

type config struct {
	funcs    map[string]any
	zeroKeys bool
}

// WithFuncs adds template functions, available in every part.
func WithFuncs(funcs map[string]any) Option {
	return func(c *config) {
		for k, v := range funcs {
			c.funcs[k] = v
		}
	}
}

// MissingKeyZero renders a missing map key as its zero value instead of
// failing. With map[string]any data the zero value is a nil interface, which
// the text parts print as "<no value>"; use map[string]string data (as
// Decorate does) to get an empty string.
func MissingKeyZero() Option { return func(c *config) { c.zeroKeys = true } }

// New returns an empty Set.
func New(opts ...Option) *Set {
	c := config{funcs: map[string]any{}}
	for _, opt := range opts {
		opt(&c)
	}
	missing := "missingkey=error"
	if c.zeroKeys {
		missing = "missingkey=zero"
	}
	return &Set{
		text:  texttemplate.New("").Funcs(c.funcs).Option(missing),
		html:  htmltemplate.New("").Funcs(c.funcs).Option(missing),
		parts: map[string]map[Part]bool{},
	}
}

// ParseFS returns a Set holding every .subject, .txt and .html file under dir
// in fsys (dir "" or "." means the root), named by their path relative to dir
// without the extension: dir/orders/shipped.txt is template
// "orders/shipped". Other files are ignored.
func ParseFS(fsys fs.FS, dir string, opts ...Option) (*Set, error) {
	s := New(opts...)
	if dir == "" {
		dir = "."
	}
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		part, ok := partOf(p)
		if !ok {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		if dir == "." {
			rel = p
		}
		return s.Add(strings.TrimSuffix(rel, "."+string(part)), part, string(src))
	})
	if err != nil {
		return nil, invalid("loading templates: " + err.Error())
	}
	return s, nil
}

// Add adds one part of a template from source. A name whose last path
// element starts with "_" is a partial: it can be included by other parts
// ({{template "_footer.html" .}} — partials are referenced by name plus
// extension) but not rendered on its own. Text partials (.subject, .txt)
// serve the subject and text parts; HTML partials serve HTML parts.
func (s *Set) Add(name string, part Part, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rendered {
		return invalid("cannot add templates after rendering")
	}
	if name == "" || (part != Subject && part != Text && part != HTML) {
		return invalid("template name is empty or part is not subject, txt or html")
	}
	full := name + "." + string(part)
	var err error
	if part == HTML {
		_, err = s.html.New(full).Parse(source)
	} else {
		_, err = s.text.New(full).Parse(source)
	}
	if err != nil {
		return invalid("parsing " + full + ": " + err.Error())
	}
	if !strings.HasPrefix(path.Base(name), "_") {
		if s.parts[name] == nil {
			s.parts[name] = map[Part]bool{}
		}
		s.parts[name][part] = true
	}
	return nil
}

// Has reports whether the set defines template name, in its base variant or
// a variant for locale.
func (s *Set) Has(name, locale string) bool {
	stem, _ := s.lookup(name, locale)
	return stem != ""
}

// Render renders template name with data.
func (s *Set) Render(name string, data any) (*Rendered, error) {
	return s.RenderLocale(name, "", data)
}

// RenderLocale renders the variant of template name for locale: for "fa-IR"
// it tries name.fa-IR, then name.fa, then name. The whole variant is used,
// never parts of different languages mixed.
func (s *Set) RenderLocale(name, locale string, data any) (*Rendered, error) {
	r, _, err := s.render(name, locale, data)
	return r, err
}

// Apply renders template name for locale ("" for the base variant) and sets
// msg.Subject, msg.Text and msg.HTML from the parts the template defines,
// leaving the other fields as they are.
func (s *Set) Apply(msg *notijet.Message, name, locale string, data any) error {
	if msg == nil {
		return invalid("message is nil")
	}
	r, parts, err := s.render(name, locale, data)
	if err != nil {
		return err
	}
	if parts[Subject] {
		msg.Subject = r.Subject
	}
	if parts[Text] {
		msg.Text = r.Text
	}
	if parts[HTML] {
		msg.HTML = r.HTML
	}
	return nil
}

// lookup returns the stem of the variant of name for locale, and its parts;
// "" when there is none. It marks the set as rendered.
func (s *Set) lookup(name, locale string) (string, map[Part]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range candidates(name, locale) {
		if p := s.parts[c]; p != nil {
			return c, p
		}
	}
	return "", nil
}

func (s *Set) render(name, locale string, data any) (*Rendered, map[Part]bool, error) {
	stem, parts := s.lookup(name, locale)
	if stem == "" {
		return nil, nil, invalid("no template named " + name)
	}
	s.mu.Lock()
	s.rendered = true
	s.mu.Unlock()

	r := &Rendered{}
	var buf bytes.Buffer
	exec := func(part Part, dst *string) error {
		if !parts[part] {
			return nil
		}
		buf.Reset()
		full := stem + "." + string(part)
		var err error
		if part == HTML {
			err = s.html.ExecuteTemplate(&buf, full, data)
		} else {
			err = s.text.ExecuteTemplate(&buf, full, data)
		}
		if err != nil {
			return invalid("rendering " + full + ": " + err.Error())
		}
		*dst = strings.TrimSpace(buf.String())
		return nil
	}
	if err := errors.Join(exec(Subject, &r.Subject), exec(Text, &r.Text), exec(HTML, &r.HTML)); err != nil {
		return nil, nil, err
	}
	// A subject is one header line.
	r.Subject = strings.Join(strings.Fields(r.Subject), " ")
	return r, parts, nil
}

// candidates lists the template stems tried for name in locale, most
// specific first.
func candidates(name, locale string) []string {
	var out []string
	for l := strings.TrimSpace(locale); l != ""; {
		out = append(out, name+"."+l)
		i := strings.LastIndexAny(l, "-_")
		if i < 0 {
			break
		}
		l = l[:i]
	}
	return append(out, name)
}

func partOf(p string) (Part, bool) {
	switch path.Ext(p) {
	case ".subject":
		return Subject, true
	case ".txt":
		return Text, true
	case ".html":
		return HTML, true
	}
	return "", false
}

// Decorate wraps p so a message whose Template names a template in s is
// rendered locally before sending: the template params are the data
// ({{.code}} for a "code" param), the rendered parts fill Subject, Text and
// HTML, and Template is cleared. Messages with any other Template, or none,
// pass through unchanged, so templates stored at the provider keep working.
// locale, when not nil, picks the variant for each message (for example from
// msg.Metadata["locale"]). The caller's message is never modified.
func Decorate(p notijet.Provider, s *Set, locale func(*notijet.Message) string) notijet.Provider {
	return &decorated{Provider: p, set: s, locale: locale}
}

type decorated struct {
	notijet.Provider
	set    *Set
	locale func(*notijet.Message) string
}

func (d *decorated) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if msg == nil || msg.Template == nil {
		return d.Provider.Send(ctx, msg)
	}
	loc := ""
	if d.locale != nil {
		loc = d.locale(msg)
	}
	if !d.set.Has(msg.Template.ID, loc) {
		return d.Provider.Send(ctx, msg)
	}
	m := msg.Clone()
	if err := d.set.Apply(m, msg.Template.ID, loc, msg.Template.Map()); err != nil {
		return nil, err
	}
	m.Template = nil
	return d.Provider.Send(ctx, m)
}

func invalid(message string) error {
	return notijet.InvalidTemplate(Name, message)
}
