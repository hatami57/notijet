package smtp

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	netsmtp "net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal in-process SMTP server for tests. It speaks enough
// of RFC 5321 for net/smtp: EHLO, STARTTLS, AUTH PLAIN/LOGIN/XOAUTH2, MAIL,
// RCPT, DATA, RSET, NOOP and QUIT.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	tls      *tls.Config
	implicit bool // TLS from the first byte

	startTLS  bool   // advertise STARTTLS
	authMechs string // advertised AUTH mechanisms; empty = no AUTH
	user      string
	pass      string
	token     string

	mailReply string            // reply to MAIL FROM; empty = 250
	rcptReply map[string]string // per-recipient reply to RCPT TO
	dataReply string            // final reply after DATA; empty = 250
	stall     bool              // never answer after the greeting

	mu    sync.Mutex
	mails []fakeMail
	auths []string // mechanisms that authenticated successfully
	ehlo  string
}

type fakeMail struct {
	from  string
	rcpts []string
	data  string
	tls   bool
}

// selfSigned returns a server certificate for localhost/127.0.0.1 and a pool
// that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func (s *fakeServer) start() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.ln = ln
	s.t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	isTLS := false
	if s.implicit {
		tc := tls.Server(conn, s.tls)
		if tc.Handshake() != nil {
			return
		}
		conn, isTLS = tc, true
	}
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(lines ...string) {
		for _, l := range lines {
			w.WriteString(l + "\r\n")
		}
		w.Flush()
	}
	readLine := func() (string, bool) {
		l, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(l, "\r\n"), true
	}
	reply("220 localhost ESMTP fake")
	if s.stall {
		time.Sleep(5 * time.Second)
		return
	}

	var cur fakeMail
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		arg := strings.TrimSpace(line[min(len(verb), len(line)):])
		switch verb {
		case "EHLO", "HELO":
			s.mu.Lock()
			s.ehlo = arg
			s.mu.Unlock()
			lines := []string{"250-localhost"}
			if s.startTLS && !isTLS {
				lines = append(lines, "250-STARTTLS")
			}
			if s.authMechs != "" {
				lines = append(lines, "250-AUTH "+s.authMechs)
			}
			reply(append(lines, "250 8BITMIME")...)
		case "STARTTLS":
			reply("220 2.0.0 ready")
			tc := tls.Server(conn, s.tls)
			if tc.Handshake() != nil {
				return
			}
			conn, isTLS = tc, true
			r, w = bufio.NewReader(conn), bufio.NewWriter(conn)
		case "AUTH":
			s.auth(arg, reply, readLine)
		case "MAIL":
			if s.mailReply != "" {
				reply(s.mailReply)
				continue
			}
			cur = fakeMail{from: between(arg), tls: isTLS}
			reply("250 2.1.0 ok")
		case "RCPT":
			addr := between(arg)
			if rep, ok := s.rcptReply[addr]; ok {
				reply(rep)
				continue
			}
			cur.rcpts = append(cur.rcpts, addr)
			reply("250 2.1.5 ok")
		case "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, ok := readLine()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			if s.dataReply != "" {
				reply(s.dataReply)
				continue
			}
			cur.data = b.String()
			s.mu.Lock()
			s.mails = append(s.mails, cur)
			s.mu.Unlock()
			reply("250 2.0.0 Ok: queued as ABC")
		case "RSET", "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 5.5.2 unknown command")
		}
	}
}

func (s *fakeServer) auth(arg string, reply func(...string), readLine func() (string, bool)) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		reply("501 syntax")
		return
	}
	decode := func(v string) string {
		b, _ := base64.StdEncoding.DecodeString(v)
		return string(b)
	}
	ok := false
	switch strings.ToUpper(fields[0]) {
	case "PLAIN":
		var resp string
		if len(fields) > 1 {
			resp = fields[1]
		} else {
			reply("334 ")
			resp, _ = readLine()
		}
		parts := strings.Split(decode(resp), "\x00")
		ok = len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass
	case "LOGIN":
		reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
		u, _ := readLine()
		reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
		pw, _ := readLine()
		ok = decode(u) == s.user && decode(pw) == s.pass
	case "XOAUTH2":
		want := "user=" + s.user + "\x01auth=Bearer " + s.token + "\x01\x01"
		ok = len(fields) > 1 && decode(fields[1]) == want
		if !ok {
			reply("334 " + base64.StdEncoding.EncodeToString([]byte(`{"status":"401"}`)))
			readLine()
		}
	}
	if !ok {
		reply("535 5.7.8 authentication failed")
		return
	}
	s.mu.Lock()
	s.auths = append(s.auths, strings.ToUpper(fields[0]))
	s.mu.Unlock()
	reply("235 2.7.0 accepted")
}

func between(s string) string {
	i, j := strings.Index(s, "<"), strings.Index(s, ">")
	if i < 0 || j < i {
		return ""
	}
	return s[i+1 : j]
}

func (s *fakeServer) sent() []fakeMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeMail(nil), s.mails...)
}

// lastEHLO returns the name the client last sent with EHLO.
func (s *fakeServer) lastEHLO() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ehlo
}

// netsmtpServerInfo builds the net/smtp ServerInfo the auth mechanisms see.
type netsmtpServerInfo struct {
	Name string
	TLS  bool
}

func (i *netsmtpServerInfo) info() *netsmtp.ServerInfo {
	return &netsmtp.ServerInfo{Name: i.Name, TLS: i.TLS}
}
