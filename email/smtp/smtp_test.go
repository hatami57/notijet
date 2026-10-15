package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, configure func(*fakeServer)) (*fakeServer, *x509.CertPool) {
	cert, pool := selfSigned(t)
	s := &fakeServer{t: t, tls: &tls.Config{Certificates: []tls.Certificate{cert}}, startTLS: true}
	if configure != nil {
		configure(s)
	}
	s.start()
	return s, pool
}

func email() *notijet.Message {
	return &notijet.Message{
		From:    "Acme <noreply@acme.test>",
		To:      []string{"a@example.com"},
		CC:      []string{"c@example.com"},
		BCC:     []string{"hidden@example.com"},
		Subject: "Hello",
		Text:    "plain body",
		HTML:    "<p>html body</p>",
	}
}

func TestStartTLSWithPlainAuth(t *testing.T) {
	s, pool := newServer(t, func(s *fakeServer) { s.authMechs = "PLAIN LOGIN"; s.user, s.pass = "u", "p" })
	p := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}), WithAuth("u", "p"))
	res, err := p.Send(context.Background(), email())
	require.NoError(t, err)

	mails := s.sent()
	require.Len(t, mails, 1)
	m := mails[0]
	assert.True(t, m.tls, "upgraded with STARTTLS")
	assert.Equal(t, "noreply@acme.test", m.from)
	assert.Equal(t, []string{"a@example.com", "c@example.com", "hidden@example.com"}, m.rcpts)
	assert.Contains(t, m.data, "Subject: Hello")
	assert.Contains(t, m.data, "plain body")
	assert.NotContains(t, m.data, "hidden@example.com", "BCC stays out of the headers")
	assert.Equal(t, []string{"PLAIN"}, s.auths)

	require.Len(t, res.Receipts, 3)
	assert.Equal(t, "smtp", res.Provider)
	assert.True(t, strings.HasPrefix(res.MessageID(), "<") && strings.HasSuffix(res.MessageID(), "@acme.test>"))
	assert.Contains(t, m.data, "Message-ID: "+res.MessageID())
	assert.Equal(t, "accepted", res.Receipts[2].Status)
}

func TestLoginAuthAndImplicitTLS(t *testing.T) {
	s, pool := newServer(t, func(s *fakeServer) {
		s.implicit, s.startTLS = true, false
		s.authMechs = "LOGIN"
		s.user, s.pass = "u", "p"
	})
	p := New("127.0.0.1", s.port(), WithSecurity(ImplicitTLS), WithTLSConfig(&tls.Config{RootCAs: pool}), WithAuth("u", "p"))
	_, err := p.Send(context.Background(), email())
	require.NoError(t, err)
	assert.Equal(t, []string{"LOGIN"}, s.auths)
	assert.True(t, s.sent()[0].tls)
}

func TestXOAuth2(t *testing.T) {
	s, pool := newServer(t, func(s *fakeServer) { s.authMechs = "PLAIN XOAUTH2"; s.user, s.token = "me@acme.test", "tok" })
	token := "tok"
	p := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}),
		WithOAuth2("me@acme.test", func(context.Context) (string, error) { return token, nil }))
	_, err := p.Send(context.Background(), email())
	require.NoError(t, err)
	assert.Equal(t, []string{"XOAUTH2"}, s.auths)

	token = "expired"
	_, err = p.Send(context.Background(), email())
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)
	assert.Equal(t, "535", notijet.ProviderCode(err))

	p = New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}),
		WithOAuth2("me@acme.test", func(context.Context) (string, error) { return "", errors.New("refresh failed") }))
	_, err = p.Send(context.Background(), email())
	assert.ErrorContains(t, err, "OAuth2 token")
}

func TestWrongPassword(t *testing.T) {
	s, pool := newServer(t, func(s *fakeServer) { s.authMechs = "PLAIN"; s.user, s.pass = "u", "p" })
	p := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}), WithAuth("u", "wrong"))
	_, err := p.Send(context.Background(), email())
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)
	assert.False(t, notijet.IsRetryable(err))
	assert.Empty(t, s.sent())
}

func TestNoTLSLocalRelay(t *testing.T) {
	s, _ := newServer(t, func(s *fakeServer) { s.startTLS = false })
	p := New("127.0.0.1", s.port(), WithSecurity(NoTLS), WithFrom("app@acme.test"))
	msg := email()
	msg.From = ""
	_, err := p.Send(context.Background(), msg)
	require.NoError(t, err)
	m := s.sent()[0]
	assert.False(t, m.tls)
	assert.Equal(t, "app@acme.test", m.from)
}

func TestCredentialsNeverInClearToRemoteHost(t *testing.T) {
	s, _ := newServer(t, func(s *fakeServer) { s.startTLS = false; s.authMechs = "PLAIN LOGIN"; s.user, s.pass = "u", "p" })
	// Pretend the server is a remote host reached without TLS.
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, s.ln.Addr().String())
	}
	for _, mechs := range []string{"PLAIN", "LOGIN"} {
		s.authMechs = mechs
		p := New("mail.remote.test", 25, WithSecurity(NoTLS), WithDialer(dial), WithAuth("u", "p"))
		_, err := p.Send(context.Background(), email())
		assert.ErrorIs(t, err, notijet.ErrUnauthorized, mechs)
		assert.ErrorContains(t, err, "unencrypted", mechs)
	}
	assert.Empty(t, s.auths)
}

func TestStartTLSRequired(t *testing.T) {
	s, _ := newServer(t, func(s *fakeServer) { s.startTLS = false })
	_, err := New("127.0.0.1", s.port()).Send(context.Background(), email())
	assert.ErrorContains(t, err, "STARTTLS")
	assert.False(t, notijet.IsRetryable(err))
}

func TestUntrustedCertificate(t *testing.T) {
	s, _ := newServer(t, nil)
	_, err := New("127.0.0.1", s.port()).Send(context.Background(), email())
	assert.ErrorContains(t, err, "not trusted")
	assert.False(t, notijet.IsRetryable(err))
}

func TestRecipientRejections(t *testing.T) {
	s, pool := newServer(t, func(s *fakeServer) {
		s.rcptReply = map[string]string{"c@example.com": "550 5.1.1 no such user"}
	})
	p := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}))
	res, err := p.Send(context.Background(), email())
	require.NoError(t, err, "delivered to the accepted recipients")
	assert.Equal(t, []string{"a@example.com", "hidden@example.com"}, s.sent()[0].rcpts)
	assert.Equal(t, "rejected: 550 5.1.1 no such user", res.Receipts[1].Status)
	assert.Empty(t, res.Receipts[1].MessageID)

	msg := email()
	msg.To, msg.BCC = nil, nil
	_, err = p.Send(context.Background(), msg)
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
	assert.Equal(t, "550", notijet.ProviderCode(err))

	s.rcptReply["c@example.com"] = "452 4.2.2 mailbox full"
	_, err = p.Send(context.Background(), msg)
	assert.True(t, notijet.IsRetryable(err), "4xx is transient")
}

func TestServerReplies(t *testing.T) {
	cases := []struct {
		configure func(*fakeServer)
		code      string
		sentinel  error
		retry     bool
	}{
		{func(s *fakeServer) { s.mailReply = "553 5.7.1 sender not allowed" }, "553", notijet.ErrInvalidSender, false},
		{func(s *fakeServer) { s.mailReply = "421 4.3.0 try later" }, "421", nil, true},
		{func(s *fakeServer) { s.dataReply = "554 5.7.1 spam" }, "554", nil, false},
		{func(s *fakeServer) { s.dataReply = "451 4.3.0 local error" }, "451", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			s, pool := newServer(t, tc.configure)
			_, err := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool})).Send(context.Background(), email())
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	s, _ := newServer(t, func(s *fakeServer) { s.stall = true })
	start := time.Now()
	_, err := New("127.0.0.1", s.port(), WithTimeout(100*time.Millisecond)).Send(context.Background(), email())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, notijet.IsRetryable(err))
	assert.Less(t, time.Since(start), 2*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = New("127.0.0.1", s.port()).Send(ctx, email())
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, notijet.IsRetryable(err))
}

func TestConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	_, err = New("127.0.0.1", port).Send(context.Background(), email())
	assert.True(t, notijet.IsRetryable(err))
	assert.NotContains(t, err.Error(), "127.0.0.1", "addresses are stripped")
}

func TestValidation(t *testing.T) {
	p := New("127.0.0.1", 1)
	msg := email()
	msg.Subject = ""
	_, err := p.Send(context.Background(), msg)
	assert.Error(t, err)
	msg = email()
	msg.From = ""
	_, err = p.Send(context.Background(), msg)
	assert.ErrorContains(t, err, "From")
	msg = email()
	msg.To = []string{"not an address"}
	_, err = p.Send(context.Background(), msg)
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
	_, err = New("", 0).Send(context.Background(), email())
	assert.Error(t, err)
	msg = email()
	msg.Template = notijet.NewTemplate("t")
	msg.Text, msg.HTML = "", ""
	_, err = p.Send(context.Background(), msg)
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
}

func TestPresetsAndDefaults(t *testing.T) {
	p := Gmail.New(WithAuth("u", "p"))
	assert.Equal(t, "smtp.gmail.com", p.host)
	assert.Equal(t, 587, p.port)
	assert.Equal(t, StartTLS, p.security)
	y := Yahoo.New()
	assert.Equal(t, ImplicitTLS, y.security)
	assert.Equal(t, NoTLS, Yahoo.New(WithSecurity(NoTLS)).security, "options override the preset")
	assert.Equal(t, ImplicitTLS, New("h", 465).security, "465 implies implicit TLS")
	assert.Equal(t, 587, New("h", 0).port)
	assert.Equal(t, "smtp", p.Name())
	assert.Equal(t, notijet.ChannelEmail, p.Channel())
}

func TestAuthNegotiation(t *testing.T) {
	cases := []struct {
		name  string
		mechs string
		opt   Option
		want  string
	}{
		{"no AUTH offered", "", WithAuth("u", "p"), "does not offer AUTH"},
		{"no usable mechanism", "CRAM-MD5", WithAuth("u", "p"), "no supported AUTH mechanism"},
		{"XOAUTH2 not offered", "PLAIN", WithOAuth2("u", func(context.Context) (string, error) { return "t", nil }), "XOAUTH2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, pool := newServer(t, func(s *fakeServer) { s.authMechs = tc.mechs })
			_, err := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}), tc.opt).Send(context.Background(), email())
			assert.ErrorIs(t, err, notijet.ErrUnauthorized)
			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, s.sent())
		})
	}
}

func TestLocalNameAndMessageIDDomain(t *testing.T) {
	s, pool := newServer(t, nil)
	p := New("127.0.0.1", s.port(), WithTLSConfig(&tls.Config{RootCAs: pool}),
		WithLocalName("app.acme.test"), WithMessageIDDomain("mail.acme.test"))
	res, err := p.Send(context.Background(), email())
	require.NoError(t, err)
	assert.Equal(t, "app.acme.test", s.lastEHLO())
	assert.True(t, strings.HasSuffix(res.MessageID(), "@mail.acme.test>"))
}

func TestImplicitTLSAgainstPlainServer(t *testing.T) {
	s, _ := newServer(t, func(s *fakeServer) { s.startTLS = false })
	_, err := New("127.0.0.1", s.port(), WithSecurity(ImplicitTLS), WithTimeout(2*time.Second)).Send(context.Background(), email())
	require.Error(t, err)
	assert.True(t, notijet.IsRetryable(err), "a failed handshake that is not a trust problem is a fault")
}

func TestAuthMechanisms(t *testing.T) {
	plain := &netsmtpServerInfo{Name: "mail.remote.test", TLS: true}
	l := &loginAuth{user: "u", pass: "p", host: "mail.remote.test"}
	mech, _, err := l.Start(plain.info())
	require.NoError(t, err)
	assert.Equal(t, "LOGIN", mech)
	b, err := l.Next([]byte("Username:"), true)
	require.NoError(t, err)
	assert.Equal(t, "u", string(b))
	b, err = l.Next([]byte("Password:"), true)
	require.NoError(t, err)
	assert.Equal(t, "p", string(b))
	_, err = l.Next([]byte("Token:"), true)
	assert.Error(t, err)
	b, err = l.Next(nil, false)
	assert.NoError(t, err)
	assert.Nil(t, b)

	_, _, err = l.Start((&netsmtpServerInfo{Name: "other.test", TLS: true}).info())
	assert.ErrorContains(t, err, "wrong host name")

	x := &xoauth2Auth{user: "u", token: "t", host: "mail.remote.test"}
	mech, resp, err := x.Start(plain.info())
	require.NoError(t, err)
	assert.Equal(t, "XOAUTH2", mech)
	assert.Equal(t, "user=u\x01auth=Bearer t\x01\x01", string(resp))
	b, _ = x.Next([]byte(`{"status":"401"}`), true)
	assert.Equal(t, []byte{}, b)
	b, _ = x.Next(nil, false)
	assert.Nil(t, b)
	_, _, err = x.Start((&netsmtpServerInfo{Name: "mail.remote.test"}).info())
	assert.ErrorContains(t, err, "unencrypted")

	assert.True(t, isLocalhost("::1"))
	assert.False(t, isLocalhost("mail.remote.test"))
}
