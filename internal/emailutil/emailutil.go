// Package emailutil holds the checks every email API provider runs before
// calling its API: validation, address parsing and custom header checks, so
// all of them accept and reject exactly the same messages as the SMTP
// provider.
package emailutil

import (
	"net/mail"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/mimex"
)

// Envelope is a message's parsed addresses.
type Envelope struct {
	From    *mail.Address
	To      []*mail.Address
	CC      []*mail.Address
	BCC     []*mail.Address
	ReplyTo []*mail.Address
}

// Prepare validates msg for an email provider (see notijet.CheckEmail), parses
// its addresses — From falling back to defaultFrom — and checks its custom
// headers. templates reports whether the provider supports server-side
// templates; useTemplate is true when the template should be sent.
func Prepare(provider string, msg *notijet.Message, defaultFrom string, templates bool) (env *Envelope, useTemplate bool, err error) {
	if useTemplate, err = notijet.CheckEmail(provider, msg, templates); err != nil {
		return nil, false, err
	}
	from := strings.TrimSpace(msg.From)
	if from == "" {
		from = strings.TrimSpace(defaultFrom)
	}
	if from == "" {
		return nil, false, notijet.Invalid(provider, "email has no From address")
	}
	env = &Envelope{}
	if env.From, err = mimex.ParseAddress(from); err != nil {
		return nil, false, notijet.InvalidSender(provider, err.Error())
	}
	for _, l := range []struct {
		dst *[]*mail.Address
		src []string
	}{{&env.To, msg.To}, {&env.CC, msg.CC}, {&env.BCC, msg.BCC}} {
		if *l.dst, err = mimex.ParseAddressList(l.src); err != nil {
			return nil, false, notijet.InvalidRecipient(provider, err.Error())
		}
	}
	if env.ReplyTo, err = mimex.ParseAddressList(msg.ReplyTo); err != nil {
		return nil, false, notijet.Invalid(provider, "Reply-To: "+err.Error())
	}
	if err := mimex.CheckHeaders(msg.Headers); err != nil {
		return nil, false, notijet.Invalid(provider, err.Error())
	}
	return env, useTemplate, nil
}

// Count returns the number of recipients across To, CC and BCC.
func (e *Envelope) Count() int { return len(e.To) + len(e.CC) + len(e.BCC) }

// Emails returns the bare addresses of list.
func Emails(list []*mail.Address) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Address
	}
	return out
}

// Display formats a as `Name <email>` (the name quoted when it needs to be),
// or the bare address when it has no name. Unlike mail.Address.String it does
// not RFC 2047-encode the name: JSON APIs take UTF-8 as is.
func Display(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	name := a.Name
	if strings.ContainsAny(name, `"(),.:;<>@[\]`) {
		name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
	}
	return name + " <" + a.Address + ">"
}

// DisplayList formats every address of list with Display.
func DisplayList(list []*mail.Address) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = Display(a)
	}
	return out
}

// Receipt is the single receipt of an email provider that returns one ID for
// the whole message.
func Receipt(id, status string) []notijet.Receipt {
	return []notijet.Receipt{{MessageID: id, Status: status}}
}

// ValidTag reports whether s may be an SES or Resend tag name or value: 1–256 ASCII letters, digits, '_' or '-'.
func ValidTag(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
