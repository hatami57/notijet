// Package smsutil holds the checks every SMS provider runs before calling its
// API: recipient normalization and sender resolution.
package smsutil

import (
	"slices"
	"strings"

	"github.com/hatami57/notijet"
)

// Recipients normalizes every recipient of msg with normalize (one of the phone
// package helpers) and drops duplicates, keeping the first occurrence's order.
// A number normalize rejects fails the whole message with an
// ErrInvalidRecipient error naming it, before anything is sent.
func Recipients(provider string, msg *notijet.Message, normalize func(string) (string, error)) ([]string, error) {
	var out []string
	for _, to := range msg.Recipients() {
		n, err := normalize(to)
		if err != nil {
			return nil, notijet.InvalidRecipient(provider, "invalid phone number "+to)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// Sender returns msg.From when set, otherwise def. required makes an empty
// result an ErrInvalidSender error, for providers without an account default.
func Sender(provider string, msg *notijet.Message, def string, required bool) (string, error) {
	s := strings.TrimSpace(msg.From)
	if s == "" {
		s = strings.TrimSpace(def)
	}
	if s == "" && required {
		return "", notijet.InvalidSender(provider, "no sender line: set Message.From or the provider's WithSender option")
	}
	return s, nil
}

// Flag reports whether msg.Data[key] is a true value ("1", "true", "yes").
func Flag(msg *notijet.Message, key string) bool {
	switch strings.ToLower(strings.TrimSpace(msg.Data[key])) {
	case "1", "true", "yes":
		return true
	}
	return false
}
