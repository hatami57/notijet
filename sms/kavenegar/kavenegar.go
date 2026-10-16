// Package kavenegar is a notijet SMS provider for Kavenegar
// (https://kavenegar.com).
//
// A plain message goes through sms/send (up to 200 receptors per request);
// a message with a Template goes through verify/lookup, one request per
// recipient. Template params map onto Kavenegar's token, token2, token3,
// token10 and token20: by name when every param uses one of those names,
// otherwise by position.
//
//	p := kavenegar.New(apiKey, kavenegar.WithSender("10004346"))
//	p.Send(ctx, &notijet.Message{
//	    To:       []string{"09121234567"},
//	    Template: notijet.NewTemplate("verify", "token", "52341"),
//	})
//
// Token values follow Kavenegar's rules, checked before any request: at most
// 100 characters, no newline or underscore, no space in token, token2 and
// token3, and at most 5 spaces in token10 and 8 in token20. Values such as
// names and dates often contain spaces; WithTokenSpaceReplacer substitutes
// them in the tokens that allow none.
//
// Set Message.Data["type"] = "call" to deliver a lookup template as a voice
// call; Kavenegar calls only with a digits-only token. Message.ID is forwarded as localid when it is numeric and the message
// has a single recipient.
package kavenegar

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/flexjson"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "kavenegar"

// DefaultBaseURL is Kavenegar's REST API root.
const DefaultBaseURL = "https://api.kavenegar.com/v1"

// maxReceptors is the most receptors sms/send accepts in one request.
const maxReceptors = 200

// tokens are the lookup parameter names, in positional order.
var tokens = []string{"token", "token2", "token3", "token10", "token20"}

// maxSpaces is how many spaces each lookup parameter may contain.
var maxSpaces = map[string]int{"token": 0, "token2": 0, "token3": 0, "token10": 5, "token20": 8}

// maxTokenLen is the most characters a lookup parameter may hold.
const maxTokenLen = 100

// Provider sends SMS through Kavenegar.
type Provider struct {
	apiKey        string
	sender        string
	baseURL       string
	spaceReplacer string // replaces spaces in tokens that allow none; empty keeps them
	client        *httpc.Client
	hc            *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender line, used when Message.From is empty.
// Without one, Kavenegar sends from the account's default line.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithTokenSpaceReplacer replaces every space in token, token2 and token3 —
// which Kavenegar rejects — with r before sending, so a value such as a name
// or a date fits; token10 and token20, which allow a few spaces, are left as
// they are. Kavenegar also rejects underscores, so pick a character the
// template renders acceptably (a hyphen, or a zero-width non-joiner U+200C
// for Persian text).
func WithTokenSpaceReplacer(r string) Option { return func(p *Provider) { p.spaceReplacer = r } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Kavenegar provider for apiKey.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{apiKey: strings.TrimSpace(apiKey), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	useTemplate, err := notijet.CheckSMS(Name, msg, true)
	if err != nil {
		return nil, err
	}
	if p.apiKey == "" {
		return nil, notijet.Invalid(Name, "API key is empty")
	}
	to, err := smsutil.Recipients(Name, msg, phone.IranLocalOrInternational)
	if err != nil {
		return nil, err
	}
	sender, _ := smsutil.Sender(Name, msg, p.sender, false)

	if useTemplate {
		params, err := lookupParams(msg.Template, p.spaceReplacer)
		if err != nil {
			return nil, err
		}
		params.Set("template", msg.Template.ID)
		if msg.Data["type"] == "call" {
			if !isDigits(params.Get("token")) {
				return nil, notijet.InvalidTemplate(Name, "a voice call needs a digits-only token")
			}
			params.Set("type", "call")
		}
		return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
			params.Set("receptor", batch[0])
			return p.call(ctx, "verify/lookup.json", params)
		})
	}

	return notijet.SendBatches(ctx, Name, to, maxReceptors, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		form := url.Values{"receptor": {strings.Join(batch, ",")}, "message": {msg.Text}}
		if sender != "" {
			form.Set("sender", sender)
		}
		if len(to) == 1 && isDigits(msg.ID) {
			form.Set("localid", msg.ID)
		}
		return p.call(ctx, "sms/send.json", form)
	})
}

// lookupParams maps template params onto Kavenegar's token fields, replacing
// spaces with spaceReplacer (when set) in the fields that allow none, and
// checks each value against Kavenegar's token rules.
func lookupParams(t *notijet.Template, spaceReplacer string) (url.Values, error) {
	if len(t.Params) > len(tokens) {
		return nil, notijet.InvalidTemplate(Name, "a lookup template takes at most 5 params (token, token2, token3, token10, token20)")
	}
	byName := true
	for _, prm := range t.Params {
		if !slices.Contains(tokens, prm.Name) {
			byName = false
			break
		}
	}
	v := url.Values{}
	for i, prm := range t.Params {
		name := tokens[i]
		if byName {
			name = prm.Name
		}
		value := prm.Value
		if spaceReplacer != "" && maxSpaces[name] == 0 {
			value = strings.ReplaceAll(value, " ", spaceReplacer)
		}
		if err := checkToken(name, value); err != nil {
			return nil, err
		}
		v.Set(name, value)
	}
	return v, nil
}

// checkToken reports a value Kavenegar would reject for the lookup field
// name (error 431), so the message fails before any request.
func checkToken(name, value string) error {
	fail := func(problem string) error {
		return notijet.InvalidTemplate(Name, name+" "+problem)
	}
	switch spaces := strings.Count(value, " "); {
	case utf8.RuneCountInString(value) > maxTokenLen:
		return fail("is longer than " + strconv.Itoa(maxTokenLen) + " characters")
	case strings.ContainsAny(value, "\r\n"):
		return fail("contains a newline")
	case strings.Contains(value, "_"):
		return fail("contains an underscore, which Kavenegar rejects")
	case spaces > maxSpaces[name] && maxSpaces[name] == 0:
		return fail("contains a space, which Kavenegar rejects there (see WithTokenSpaceReplacer, or use token10 or token20)")
	case spaces > maxSpaces[name]:
		return fail("contains more than " + strconv.Itoa(maxSpaces[name]) + " spaces")
	}
	return nil
}

type reply struct {
	Return struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	} `json:"return"`
	Entries []struct {
		MessageID  flexjson.String `json:"messageid"`
		StatusText string          `json:"statustext"`
		Receptor   string          `json:"receptor"`
		Cost       flexjson.String `json:"cost"`
	} `json:"entries"`
}

func (p *Provider) call(ctx context.Context, method string, form url.Values) ([]notijet.Receipt, error) {
	endpoint := p.baseURL + "/" + url.PathEscape(p.apiKey) + "/" + method
	resp, err := p.client.Do(ctx, httpc.Form(http.MethodPost, endpoint, form))
	if err != nil {
		return nil, err
	}
	var r reply
	if err := p.client.Decode(resp, &r); err != nil {
		return nil, err
	}
	if r.Return.Status != 200 {
		return nil, mapError(r.Return.Status, r.Return.Message)
	}
	if !resp.OK() || len(r.Entries) == 0 {
		return nil, p.client.Unexpected(resp, "")
	}
	receipts := make([]notijet.Receipt, len(r.Entries))
	for i, e := range r.Entries {
		receipts[i] = notijet.Receipt{
			To:        e.Receptor,
			MessageID: string(e.MessageID),
			Status:    e.StatusText,
			Cost:      string(e.Cost),
		}
	}
	return receipts, nil
}

// mapError turns a Kavenegar return.status into a notijet error.
func mapError(status int, message string) error {
	code := strconv.Itoa(status)
	switch status {
	case 401, 403, 407, 416:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case 411:
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	case 412:
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case 418:
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case 424, 428, 431, 432:
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	case 409:
		return notijet.FaultCode(Name, code, message, nil)
	case 451:
		return notijet.Throttled(Name, code, message)
	}
	return notijet.Rejected(Name, code, message, nil)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}
