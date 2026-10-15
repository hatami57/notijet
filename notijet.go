// Package notijet is a multi-provider notification SDK: SMS gateways (Iranian
// and international), email services, chat bots, push services and webhooks all
// implement the Provider interface, so applications can swap providers, fail
// over between them and add new platforms without changing call sites.
//
// A Message is sent through a Provider:
//
//	p := kavenegar.New(apiKey, kavenegar.WithSender("10004346"))
//	res, err := p.Send(ctx, &notijet.Message{
//	    To:   []string{"09121234567"},
//	    Text: "Your order has shipped.",
//	})
//
// or through a Notifier, which holds named providers and a default per channel.
// Errors are microjet errorx.Error values; see errors.go.
package notijet

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
)

// DefaultTimeout is the timeout of the HTTP client each provider uses by
// default. Override it per provider with the package's WithHTTPClient option.
const DefaultTimeout = 30 * time.Second

// DefaultHTTPClient returns a new *http.Client with DefaultTimeout. Providers
// use it when no custom client is supplied, so a hung upstream cannot block a
// goroutine forever.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: DefaultTimeout}
}

// Channel is the medium a provider delivers through.
type Channel string

// The channels notijet ships providers for. A custom provider may define its own.
const (
	ChannelSMS     Channel = "sms"
	ChannelEmail   Channel = "email"
	ChannelChat    Channel = "chat"
	ChannelPush    Channel = "push"
	ChannelWebhook Channel = "webhook"
)

// Provider is the interface every notification platform implements.
type Provider interface {
	// Name identifies the platform ("kavenegar", "smtp", "telegram", ...). It is
	// the Subject of the errors the provider returns and the value recorded in
	// Result.Provider.
	Name() string
	// Channel reports the medium the provider delivers through.
	Channel() Channel
	// Send delivers msg. A provider rejects message features it cannot honour
	// (an attachment on SMS, a template on a provider without one) with an
	// ErrUnsupported error rather than silently dropping them.
	Send(ctx context.Context, msg *Message) (*Result, error)
}

// Message is one notification. Which fields matter depends on the channel:
//
//   - SMS: To (phone numbers), From (sender line, optional where the provider
//     has a default), and either Text or Template.
//   - Email: To/CC/BCC (addresses), From, Subject, Text and/or HTML,
//     Attachments, ReplyTo and Headers.
//   - Chat: To (chat IDs; webhooks need none), Text or HTML.
//   - Push: To (device tokens or topics), Subject (title), Text (body), Data.
//
// Recipients that are blank after trimming are dropped, so a list assembled
// from optional fields needs no cleanup.
type Message struct {
	// ID is your own identifier for the message. Providers that accept a client
	// reference forward it (Kavenegar localid, Ghasedak clientReferenceId,
	// message tags); the Queue uses it as the record ID when set.
	ID string

	To   []string
	From string

	Subject string
	Text    string
	HTML    string

	// Template sends a template stored at the provider (a Kavenegar lookup
	// template, an SMS.ir verify template, an IPPanel pattern, ...) instead of
	// Text. Only providers with server-side templates accept it.
	Template *Template

	CC          []string
	BCC         []string
	ReplyTo     []string
	Attachments []Attachment
	// Headers are extra email headers (e.g. List-Unsubscribe).
	Headers map[string]string

	// Data is a structured payload for channels that carry one: the data map of
	// a push notification, extra fields of a webhook body, or provider-specific
	// knobs documented on each provider (e.g. Telegram "parse_mode").
	Data map[string]string
	// Metadata are tags attached to the message where the provider supports
	// them (SES tags, Mailgun variables, Postmark metadata ...), for correlating
	// delivery events with your own records.
	Metadata map[string]string
}

// Template refers to a template stored at the provider, with the values to fill
// it. Params are ordered: providers that take named values (Kavenegar token,
// token2 ...; SMS.ir; IPPanel) use the names, and providers that take
// positional values (Melipayamak BaseServiceNumber) use the order.
type Template struct {
	// ID is the provider's template identifier: a name, a numeric ID or a
	// pattern code, as the provider's panel shows it.
	ID     string
	Params []Param
}

// Param is one named template value.
type Param struct {
	Name  string
	Value string
}

// NewTemplate builds a Template from alternating names and values:
//
//	notijet.NewTemplate("verify", "code", "12345", "name", "Ali")
//
// A trailing name with no value gets an empty value.
func NewTemplate(id string, nameValues ...string) *Template {
	t := &Template{ID: id}
	for i := 0; i < len(nameValues); i += 2 {
		p := Param{Name: nameValues[i]}
		if i+1 < len(nameValues) {
			p.Value = nameValues[i+1]
		}
		t.Params = append(t.Params, p)
	}
	return t
}

// Get returns the value of the named parameter, or "" if it is not set.
func (t *Template) Get(name string) string {
	for _, p := range t.Params {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// Values returns the parameter values in order.
func (t *Template) Values() []string {
	values := make([]string, len(t.Params))
	for i, p := range t.Params {
		values[i] = p.Value
	}
	return values
}

// Map returns the parameters as a map of name to value.
func (t *Template) Map() map[string]string {
	m := make(map[string]string, len(t.Params))
	for _, p := range t.Params {
		m[p.Name] = p.Value
	}
	return m
}

// Attachment is a file attached to an email.
type Attachment struct {
	Filename string
	// ContentType is the MIME type; empty means it is guessed from Filename,
	// falling back to application/octet-stream.
	ContentType string
	Content     []byte
	// ContentID, when set, makes the attachment inline: reference it from the
	// HTML body as <img src="cid:ContentID">.
	ContentID string
}

// Inline reports whether the attachment is an inline (cid:) part.
func (a Attachment) Inline() bool { return a.ContentID != "" }

// Result is returned by a successful Provider.Send.
type Result struct {
	// Provider is the Name of the provider that delivered the message. Behind a
	// Failover it tells which one succeeded.
	Provider string
	// Receipts holds what the provider reported per recipient, in the order of
	// Message.To where the provider reports per recipient, or a single receipt
	// for the whole message where it reports one ID.
	Receipts []Receipt
}

// Receipt is what a provider reported for one recipient.
type Receipt struct {
	To string
	// MessageID is the provider's identifier for the message, used to query
	// delivery status or correlate delivery reports.
	MessageID string
	// Status is the provider's own status label, when it reports one.
	Status string
	// Cost is the provider-reported price, as the provider formats it.
	Cost string
}

// MessageID returns the first receipt's message ID, or "" when there is none.
// It is the ID to keep for providers that return one per message.
func (r *Result) MessageID() string {
	if r == nil || len(r.Receipts) == 0 {
		return ""
	}
	return r.Receipts[0].MessageID
}

// MessageIDs returns every non-empty receipt message ID.
func (r *Result) MessageIDs() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, rc := range r.Receipts {
		if rc.MessageID != "" {
			ids = append(ids, rc.MessageID)
		}
	}
	return ids
}

// Recipients returns msg.To trimmed, with blank entries dropped.
func (m *Message) Recipients() []string { return CleanList(m.To) }

// HasBody reports whether the message carries any content to send: text, HTML
// or a provider template.
func (m *Message) HasBody() bool {
	return strings.TrimSpace(m.Text) != "" || strings.TrimSpace(m.HTML) != "" || m.Template != nil
}

// Validate reports whether the message has the fields every provider requires:
// at least one recipient and some content. Providers call it before any network
// request; channel-specific checks follow in each provider.
func (m *Message) Validate() error {
	switch {
	case m == nil:
		return Invalid("message", "message is nil")
	case len(m.Recipients()) == 0:
		return Invalid("message", "message has no recipient")
	case !m.HasBody():
		return Invalid("message", "message has no Text, HTML or Template")
	case m.Template != nil && strings.TrimSpace(m.Template.ID) == "":
		return Invalid("message", "Template.ID is required")
	}
	return nil
}

// Clone returns a deep copy of the message, so a decorator or hook can adjust
// one without affecting the caller's value.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	c := *m
	c.To = slices.Clone(m.To)
	c.CC = slices.Clone(m.CC)
	c.BCC = slices.Clone(m.BCC)
	c.ReplyTo = slices.Clone(m.ReplyTo)
	if m.Template != nil {
		t := *m.Template
		t.Params = slices.Clone(m.Template.Params)
		c.Template = &t
	}
	if m.Attachments != nil {
		c.Attachments = make([]Attachment, len(m.Attachments))
		for i, a := range m.Attachments {
			a.Content = slices.Clone(a.Content)
			c.Attachments[i] = a
		}
	}
	c.Headers = cloneMap(m.Headers)
	c.Data = cloneMap(m.Data)
	c.Metadata = cloneMap(m.Metadata)
	return &c
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// CleanList trims each entry and drops blank ones. It returns nil for a list
// with no non-blank entry.
func CleanList(list []string) []string {
	var out []string
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
