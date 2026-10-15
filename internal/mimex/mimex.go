// Package mimex builds RFC 5322 / MIME email messages from a notijet.Message.
// The SMTP provider sends the bytes as they are; the Gmail API and SES raw
// sends wrap them. Every header value is checked for CR/LF so a message field
// can never inject a header.
package mimex

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hatami57/notijet"
)

// Options tunes Build.
type Options struct {
	// From is the sender address used when the message has none.
	From string
	// IncludeBCC writes a Bcc header. SMTP must not (every recipient would see
	// it); the Gmail API needs it to know the BCC recipients and strips it
	// before delivery.
	IncludeBCC bool
	// Date stamps the Date header; zero means now.
	Date time.Time
	// MessageIDDomain is the domain of the generated Message-ID; empty means the
	// sender's domain.
	MessageIDDomain string
}

// Message is a built email.
type Message struct {
	// From is the bare sender address (envelope MAIL FROM).
	From string
	// Recipients are the bare addresses of To, CC and BCC (envelope RCPT TO).
	Recipients []string
	// MessageID is the generated Message-ID header value, angle brackets included.
	MessageID string
	// Bytes is the complete message, headers and body, with CRLF line endings.
	Bytes []byte
}

// ParseAddress parses "user@example.com" or `"Name" <user@example.com>`
// (non-ASCII names allowed).
func ParseAddress(s string) (*mail.Address, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("invalid email address %q: %w", s, err)
	}
	return a, nil
}

// ParseAddressList parses every non-blank entry of list.
func ParseAddressList(list []string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, s := range notijet.CleanList(list) {
		a, err := ParseAddress(s)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Build renders msg as a MIME message. provider is the subject of the errors it
// returns. The body structure follows what is set:
//
//	multipart/mixed            (when there are regular attachments)
//	└ multipart/related        (when there are inline attachments)
//	  └ multipart/alternative  (when there are both Text and HTML)
//	    ├ text/plain
//	    └ text/html
func Build(provider string, msg *notijet.Message, opts Options) (*Message, error) {
	fromStr := msg.From
	if strings.TrimSpace(fromStr) == "" {
		fromStr = opts.From
	}
	if strings.TrimSpace(fromStr) == "" {
		return nil, notijet.Invalid(provider, "email has no From address")
	}
	from, err := ParseAddress(fromStr)
	if err != nil {
		return nil, notijet.InvalidSender(provider, err.Error())
	}
	to, err := ParseAddressList(msg.To)
	if err != nil {
		return nil, notijet.InvalidRecipient(provider, err.Error())
	}
	cc, err := ParseAddressList(msg.CC)
	if err != nil {
		return nil, notijet.InvalidRecipient(provider, err.Error())
	}
	bcc, err := ParseAddressList(msg.BCC)
	if err != nil {
		return nil, notijet.InvalidRecipient(provider, err.Error())
	}
	replyTo, err := ParseAddressList(msg.ReplyTo)
	if err != nil {
		return nil, notijet.Invalid(provider, err.Error())
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return nil, notijet.Invalid(provider, "message has no recipient")
	}

	out := &Message{From: from.Address}
	for _, list := range [][]*mail.Address{to, cc, bcc} {
		for _, a := range list {
			if !slices.Contains(out.Recipients, a.Address) {
				out.Recipients = append(out.Recipients, a.Address)
			}
		}
	}

	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	domain := opts.MessageIDDomain
	if domain == "" {
		domain = from.Address[strings.LastIndex(from.Address, "@")+1:]
	}
	out.MessageID = "<" + randomHex(16) + "@" + domain + ">"

	var buf bytes.Buffer
	h := headerWriter{buf: &buf}
	h.add("From", from.String())
	h.addList("To", to)
	h.addList("Cc", cc)
	if opts.IncludeBCC {
		h.addList("Bcc", bcc)
	}
	h.addList("Reply-To", replyTo)
	h.add("Subject", mime.BEncoding.Encode("utf-8", msg.Subject))
	h.add("Date", date.Format(time.RFC1123Z))
	h.add("Message-ID", out.MessageID)
	h.add("MIME-Version", "1.0")
	if err := CheckHeaders(msg.Headers); err != nil {
		return nil, notijet.Invalid(provider, err.Error())
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Headers)) {
		h.add(k, msg.Headers[k])
	}
	if h.err != nil {
		return nil, notijet.Invalid(provider, h.err.Error())
	}

	var inline, attached []notijet.Attachment
	for _, a := range msg.Attachments {
		if a.Inline() {
			inline = append(inline, a)
		} else {
			attached = append(attached, a)
		}
	}

	if err := writeBody(&buf, msg, inline, attached); err != nil {
		return nil, notijet.Invalid(provider, err.Error())
	}
	out.Bytes = buf.Bytes()
	return out, nil
}

// reserved are the headers Build derives from the message fields; a custom
// header may not override them.
var reserved = map[string]bool{
	"From": true, "To": true, "Cc": true, "Bcc": true, "Reply-To": true,
	"Subject": true, "Date": true, "Message-Id": true, "Mime-Version": true,
	"Content-Type": true, "Content-Transfer-Encoding": true,
}

type headerWriter struct {
	buf *bytes.Buffer
	err error
}

func (h *headerWriter) add(key, value string) {
	if h.err != nil {
		return
	}
	if strings.ContainsAny(key, "\r\n: ") || strings.ContainsAny(value, "\r\n") {
		h.err = fmt.Errorf("header %q contains a line break or is malformed", key)
		return
	}
	fmt.Fprintf(h.buf, "%s: %s\r\n", key, value)
}

func (h *headerWriter) addList(key string, list []*mail.Address) {
	if len(list) == 0 {
		return
	}
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	h.add(key, strings.Join(parts, ", "))
}

// part is the content of one MIME node: its headers and a function writing its
// body.
type part struct {
	header textproto.MIMEHeader
	body   func(*bytes.Buffer) error
}

func writeBody(buf *bytes.Buffer, msg *notijet.Message, inline, attached []notijet.Attachment) error {
	root := alternative(msg)
	if len(inline) > 0 {
		root = multi("related", append([]part{root}, attachmentParts(inline)...))
	}
	if len(attached) > 0 {
		root = multi("mixed", append([]part{root}, attachmentParts(attached)...))
	}
	for _, k := range sortedKeys(root.header) {
		for _, v := range root.header[k] {
			fmt.Fprintf(buf, "%s: %s\r\n", k, v)
		}
	}
	buf.WriteString("\r\n")
	return root.body(buf)
}

func alternative(msg *notijet.Message) part {
	text, html := msg.Text, msg.HTML
	switch {
	case text != "" && html != "":
		return multi("alternative", []part{textPart("plain", text), textPart("html", html)})
	case html != "":
		return textPart("html", html)
	default:
		return textPart("plain", text)
	}
}

func textPart(subtype, content string) part {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "text/"+subtype+"; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	return part{header: h, body: func(b *bytes.Buffer) error {
		w := quotedprintable.NewWriter(b)
		if _, err := w.Write([]byte(normalizeNewlines(content))); err != nil {
			return err
		}
		return w.Close()
	}}
}

func attachmentParts(list []notijet.Attachment) []part {
	parts := make([]part, len(list))
	for i, a := range list {
		h := textproto.MIMEHeader{}
		name := a.Filename
		if name == "" {
			name = a.ContentID
		}
		mediaType, params := splitMediaType(ContentType(a))
		params["name"] = name
		h.Set("Content-Type", mime.FormatMediaType(mediaType, params))
		h.Set("Content-Transfer-Encoding", "base64")
		disposition := "attachment"
		if a.Inline() {
			disposition = "inline"
			h.Set("Content-ID", "<"+strings.Trim(a.ContentID, "<>")+">")
		}
		h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
		content := a.Content
		parts[i] = part{header: h, body: func(b *bytes.Buffer) error {
			writeBase64(b, content)
			return nil
		}}
	}
	return parts
}

// ContentType returns the attachment's MIME type: its ContentType, else the
// type registered for its file extension, else application/octet-stream.
func ContentType(a notijet.Attachment) string {
	ct := strings.TrimSpace(a.ContentType)
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(a.Filename))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return ct
}

// splitMediaType splits "text/plain; charset=utf-8" into its type and
// parameters, so more parameters can be added before formatting it again. A
// value that does not parse is treated as application/octet-stream.
func splitMediaType(ct string) (string, map[string]string) {
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return "application/octet-stream", map[string]string{}
	}
	return mediaType, params
}

// CheckHeaders reports a custom header that would corrupt or override the
// message: a malformed name, a value with a line break, or a header derived
// from the message fields (From, To, Subject ...). Providers that pass
// Message.Headers to an API call it so they reject exactly what Build rejects.
func CheckHeaders(headers map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(headers)) {
		if reserved[textproto.CanonicalMIMEHeaderKey(k)] {
			return fmt.Errorf("header %s is set from the message fields", k)
		}
		if k == "" || strings.ContainsAny(k, "\r\n: ") || strings.ContainsAny(headers[k], "\r\n") {
			return fmt.Errorf("header %q contains a line break or is malformed", k)
		}
	}
	return nil
}

func multi(subtype string, children []part) part {
	boundary := "notijet-" + randomHex(12)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "multipart/"+subtype+"; boundary="+boundary)
	return part{header: h, body: func(b *bytes.Buffer) error {
		w := multipart.NewWriter(b)
		if err := w.SetBoundary(boundary); err != nil {
			return err
		}
		for _, c := range children {
			var body bytes.Buffer
			if err := c.body(&body); err != nil {
				return err
			}
			pw, err := w.CreatePart(c.header)
			if err != nil {
				return err
			}
			if _, err := pw.Write(body.Bytes()); err != nil {
				return err
			}
		}
		return w.Close()
	}}
}

// writeBase64 writes data base64-encoded in 76-character lines (RFC 2045).
func writeBase64(b *bytes.Buffer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	if enc != "" {
		b.WriteString(enc)
		b.WriteString("\r\n")
	}
}

// normalizeNewlines turns bare LF and CR into CRLF, as SMTP requires.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func sortedKeys(h textproto.MIMEHeader) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
