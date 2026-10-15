// Package chatutil holds the checks shared by chat providers that post
// through an incoming webhook (Slack, Discord).
package chatutil

import (
	"strings"

	"github.com/hatami57/notijet"
)

// CheckWebhook validates msg for an incoming webhook: the common chat checks,
// no recipients (a webhook posts to its own channel) and a Text body (HTML is
// not supported).
func CheckWebhook(provider string, msg *notijet.Message) error {
	if err := notijet.CheckChat(provider, msg, false); err != nil {
		return err
	}
	if len(msg.Recipients()) > 0 {
		return notijet.Unsupported(provider, "recipients (a webhook posts to its own channel)")
	}
	if strings.TrimSpace(msg.Text) == "" {
		return notijet.Unsupported(provider, "HTML body (set Text)")
	}
	return nil
}

// Text returns msg.Text with msg.Subject, when set, as a first line.
func Text(msg *notijet.Message) string {
	if s := strings.TrimSpace(msg.Subject); s != "" {
		return s + "\n\n" + msg.Text
	}
	return msg.Text
}
