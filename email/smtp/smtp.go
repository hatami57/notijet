// Package smtp is a notijet email provider that delivers through any SMTP
// server: a mailbox provider (presets for Gmail, Outlook, Office 365, Yahoo,
// Zoho, iCloud and Yandex), a transactional relay, or a local MTA.
//
//	p := smtp.Gmail.New(smtp.WithAuth("me@gmail.com", appPassword))
//	p.Send(ctx, &notijet.Message{
//	    From:    "Acme <me@gmail.com>",
//	    To:      []string{"user@example.com"},
//	    Subject: "Welcome",
//	    Text:    "Hello!",
//	    HTML:    "<p>Hello!</p>",
//	})
//
// Each Send opens a connection, secures it (STARTTLS by default, implicit TLS
// on port 465), authenticates with PLAIN, LOGIN or XOAUTH2 — whichever the
// server offers and the credentials allow — and delivers the message built by
// notijet's MIME builder. BCC recipients get the message without appearing in
// its headers. Credentials are never sent over an unencrypted connection to a
// remote host.
//
// The receipt's MessageID is the Message-ID header of the sent message. When
// the server refuses some recipients but accepts others, the message is
// delivered to the accepted ones and each refused recipient's receipt carries
// a "rejected: …" status; when it refuses all of them, Send fails.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	netsmtp "net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "smtp"

// Security is how the connection is protected.
type Security int

const (
	// StartTLS connects in plain text and upgrades with STARTTLS, failing when
	// the server does not offer it. It is the default except on port 465.
	StartTLS Security = iota
	// ImplicitTLS speaks TLS from the first byte ("SMTPS", port 465).
	ImplicitTLS
	// NoTLS never encrypts. Use it only for a relay on localhost or a trusted
	// network; credentials are refused on such a connection to a remote host.
	NoTLS
)

// Preset holds the connection settings of a well-known mailbox provider. Most
// of them need an app password (or OAuth2, see WithOAuth2) rather than the
// account password.
type Preset struct {
	Host     string
	Port     int
	Security Security
}

// Presets for common mailbox providers.
var (
	Gmail     = Preset{"smtp.gmail.com", 587, StartTLS}
	Outlook   = Preset{"smtp-mail.outlook.com", 587, StartTLS}
	Office365 = Preset{"smtp.office365.com", 587, StartTLS}
	Yahoo     = Preset{"smtp.mail.yahoo.com", 465, ImplicitTLS}
	Zoho      = Preset{"smtp.zoho.com", 465, ImplicitTLS}
	ICloud    = Preset{"smtp.mail.me.com", 587, StartTLS}
	Yandex    = Preset{"smtp.yandex.com", 465, ImplicitTLS}
)

// New returns a provider for the preset's server; opts may override anything.
func (ps Preset) New(opts ...Option) *Provider {
	return New(ps.Host, ps.Port, append([]Option{WithSecurity(ps.Security)}, opts...)...)
}

// Provider sends email over SMTP.
type Provider struct {
	host        string
	port        int
	security    Security
	securitySet bool
	tlsConfig   *tls.Config
	user, pass  string
	oauthToken  func(ctx context.Context) (string, error)
	from        string
	localName   string
	idDomain    string
	timeout     time.Duration
	dial        func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Option configures a Provider.
type Option func(*Provider)

// WithAuth authenticates with a username and password (PLAIN or LOGIN).
func WithAuth(user, password string) Option {
	return func(p *Provider) { p.user, p.pass = user, password }
}

// WithOAuth2 authenticates with XOAUTH2 (Gmail, Outlook/Office 365), calling
// token for a fresh access token on every send — plug in an
// oauth2.TokenSource there.
func WithOAuth2(user string, token func(ctx context.Context) (string, error)) Option {
	return func(p *Provider) { p.user, p.oauthToken = user, token }
}

// WithSecurity sets how the connection is protected (default StartTLS, or
// ImplicitTLS on port 465).
func WithSecurity(s Security) Option {
	return func(p *Provider) { p.security, p.securitySet = s, true }
}

// WithTLSConfig sets the TLS configuration (default: verify the server
// certificate against the system roots for the host name).
func WithTLSConfig(c *tls.Config) Option { return func(p *Provider) { p.tlsConfig = c } }

// WithFrom sets the From address used when Message.From is empty.
func WithFrom(from string) Option { return func(p *Provider) { p.from = from } }

// WithLocalName sets the name sent with EHLO (default "localhost").
func WithLocalName(name string) Option { return func(p *Provider) { p.localName = name } }

// WithMessageIDDomain sets the domain of generated Message-IDs (default the
// sender's domain).
func WithMessageIDDomain(domain string) Option { return func(p *Provider) { p.idDomain = domain } }

// WithTimeout bounds a whole send, from dialing to QUIT, when ctx has no
// earlier deadline (default notijet.DefaultTimeout).
func WithTimeout(d time.Duration) Option { return func(p *Provider) { p.timeout = d } }

// WithDialer sets the function that opens connections, for proxies or tests
// (default a net.Dialer).
func WithDialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(p *Provider) { p.dial = dial }
}

// New returns a provider for the SMTP server at host:port. Port 0 means 587.
func New(host string, port int, opts ...Option) *Provider {
	if port == 0 {
		port = 587
	}
	p := &Provider{host: strings.TrimSpace(host), port: port, localName: "localhost", timeout: notijet.DefaultTimeout}
	for _, opt := range opts {
		opt(p)
	}
	if !p.securitySet && p.port == 465 {
		p.security = ImplicitTLS
	}
	if p.dial == nil {
		p.dial = (&net.Dialer{}).DialContext
	}
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelEmail }

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckEmail(Name, msg, false); err != nil {
		return nil, err
	}
	if p.host == "" {
		return nil, notijet.Invalid(Name, "SMTP host is empty")
	}
	built, err := mimex.Build(Name, msg, mimex.Options{From: p.from, MessageIDDomain: p.idDomain})
	if err != nil {
		return nil, err
	}
	var token string
	if p.oauthToken != nil {
		if token, err = p.oauthToken(ctx); err != nil {
			return nil, notijet.Fault(Name, "fetching OAuth2 token failed", err)
		}
	}

	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	res, err := p.deliver(ctx, built, token)
	if err != nil {
		// A deadline or cancellation cut the conversation short; report that
		// rather than the resulting I/O error. The connection deadline is the
		// context deadline, so the I/O error can surface a moment before
		// ctx.Err() is set.
		cause := ctx.Err()
		if dl, ok := ctx.Deadline(); cause == nil && ok && !time.Now().Before(dl) {
			cause = context.DeadlineExceeded
		}
		if cause != nil {
			return nil, notijet.Fault(Name, "SMTP conversation interrupted", cause)
		}
	}
	return res, err
}

func (p *Provider) deliver(ctx context.Context, m *mimex.Message, token string) (*notijet.Result, error) {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	conn, err := p.dial(ctx, "tcp", addr)
	if err != nil {
		return nil, notijet.Fault(Name, "connecting to SMTP server failed", stripAddr(err))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	tlsConfig := p.tls()
	if p.security == ImplicitTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, tlsError(err)
		}
		conn = tc
	}

	c, err := netsmtp.NewClient(conn, p.host)
	if err != nil {
		return nil, mapError(stageConnect, err)
	}
	defer c.Close()
	if err := c.Hello(p.localName); err != nil {
		return nil, mapError(stageConnect, err)
	}
	if p.security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return nil, notijet.Rejected(Name, "", "server does not offer STARTTLS (use WithSecurity to change the mode)", nil)
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			var te *textproto.Error
			if errors.As(err, &te) {
				return nil, mapError(stageConnect, err)
			}
			return nil, tlsError(err)
		}
	}
	if err := p.auth(c, token); err != nil {
		return nil, err
	}

	if err := c.Mail(m.From); err != nil {
		return nil, mapError(stageMail, err)
	}
	res := &notijet.Result{Provider: Name}
	accepted := 0
	var firstErr error
	for _, rcpt := range m.Recipients {
		rc := notijet.Receipt{To: rcpt, MessageID: m.MessageID, Status: "accepted"}
		if err := c.Rcpt(rcpt); err != nil {
			var te *textproto.Error
			if !errors.As(err, &te) {
				return nil, mapError(stageRcpt, err)
			}
			if firstErr == nil {
				firstErr = mapError(stageRcpt, err)
			}
			rc.MessageID = ""
			rc.Status = "rejected: " + strconv.Itoa(te.Code) + " " + te.Msg
		} else {
			accepted++
		}
		res.Receipts = append(res.Receipts, rc)
	}
	if accepted == 0 {
		return nil, firstErr
	}

	w, err := c.Data()
	if err != nil {
		return nil, mapError(stageData, err)
	}
	if _, err := w.Write(m.Bytes); err != nil {
		return nil, mapError(stageData, err)
	}
	if err := w.Close(); err != nil {
		return nil, mapError(stageData, err)
	}
	_ = c.Quit()
	return res, nil
}

func (p *Provider) tls() *tls.Config {
	if p.tlsConfig != nil {
		c := p.tlsConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = p.host
		}
		return c
	}
	return &tls.Config{ServerName: p.host, MinVersion: tls.VersionTLS12}
}

// auth authenticates when credentials are configured, choosing the mechanism
// from those the server advertises.
func (p *Provider) auth(c *netsmtp.Client, token string) error {
	if p.user == "" && token == "" {
		return nil
	}
	ok, mechs := c.Extension("AUTH")
	if !ok {
		return notijet.Rejected(Name, "", "server does not offer AUTH", notijet.ErrUnauthorized)
	}
	offered := strings.Fields(strings.ToUpper(mechs))
	has := func(m string) bool {
		for _, o := range offered {
			if o == m {
				return true
			}
		}
		return false
	}
	var a netsmtp.Auth
	switch {
	case token != "":
		if !has("XOAUTH2") {
			return notijet.Rejected(Name, "", "server does not offer XOAUTH2", notijet.ErrUnauthorized)
		}
		a = &xoauth2Auth{user: p.user, token: token, host: p.host}
	case has("PLAIN"):
		a = netsmtp.PlainAuth("", p.user, p.pass, p.host)
	case has("LOGIN"):
		a = &loginAuth{user: p.user, pass: p.pass, host: p.host}
	default:
		return notijet.Rejected(Name, "", "server offers no supported AUTH mechanism ("+mechs+")", notijet.ErrUnauthorized)
	}
	if err := c.Auth(a); err != nil {
		var te *textproto.Error
		if errors.As(err, &te) {
			return mapError(stageAuth, err)
		}
		// Refused locally, e.g. credentials over an unencrypted connection.
		return notijet.Rejected(Name, "", "authentication not attempted: "+err.Error(), notijet.ErrUnauthorized)
	}
	return nil
}

type stage int

const (
	stageConnect stage = iota
	stageAuth
	stageMail
	stageRcpt
	stageData
)

// mapError turns an SMTP reply (or a transport error) at the given stage into
// a notijet error. 4xx replies are transient and retryable; 5xx are permanent.
func mapError(st stage, err error) error {
	var te *textproto.Error
	if !errors.As(err, &te) {
		return notijet.Fault(Name, "SMTP conversation failed", stripAddr(err))
	}
	code := strconv.Itoa(te.Code)
	msg := te.Msg
	switch {
	case te.Code >= 400 && te.Code < 500:
		return notijet.FaultCode(Name, code, msg, nil)
	case te.Code == 530 || te.Code == 534 || te.Code == 535 || te.Code == 538 || st == stageAuth:
		return notijet.Rejected(Name, code, msg, notijet.ErrUnauthorized)
	case st == stageMail && (te.Code == 550 || te.Code == 553 || te.Code == 501 || te.Code == 555):
		return notijet.Rejected(Name, code, msg, notijet.ErrInvalidSender)
	case st == stageRcpt && (te.Code == 550 || te.Code == 551 || te.Code == 553 || te.Code == 501):
		return notijet.Rejected(Name, code, msg, notijet.ErrInvalidRecipient)
	}
	return notijet.Rejected(Name, code, msg, nil)
}

// tlsError reports a failed TLS handshake. A certificate the client does not
// trust is a configuration problem, not a transient fault.
func tlsError(err error) error {
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return notijet.Rejected(Name, "", "server certificate not trusted: "+err.Error(), nil)
	}
	return notijet.Fault(Name, "TLS handshake failed", err)
}

// stripAddr drops the address from a dial error, keeping the cause.
func stripAddr(err error) error {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err
	}
	return err
}

// loginAuth implements the LOGIN mechanism, which net/smtp lacks.
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *netsmtp.ServerInfo) (string, []byte, error) {
	if err := checkServer(server, a.host); err != nil {
		return "", nil, err
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(strings.TrimSpace(string(fromServer)))
	switch {
	case strings.HasPrefix(prompt, "user"):
		return []byte(a.user), nil
	case strings.HasPrefix(prompt, "pass"):
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected LOGIN challenge " + strconv.Quote(string(fromServer)))
}

// xoauth2Auth implements the XOAUTH2 mechanism used by Gmail and Microsoft.
type xoauth2Auth struct{ user, token, host string }

func (a *xoauth2Auth) Start(server *netsmtp.ServerInfo) (string, []byte, error) {
	if err := checkServer(server, a.host); err != nil {
		return "", nil, err
	}
	return "XOAUTH2", []byte("user=" + a.user + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

// Next answers the server's error challenge with an empty response, as the
// mechanism requires, so the server sends its final error reply.
func (a *xoauth2Auth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}

// checkServer refuses to send credentials in clear text to a remote host or to
// a server other than the one configured, like net/smtp's PlainAuth.
func checkServer(server *netsmtp.ServerInfo, host string) error {
	if !server.TLS && !isLocalhost(server.Name) {
		return errors.New("unencrypted connection")
	}
	if server.Name != host {
		return errors.New("wrong host name")
	}
	return nil
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}
