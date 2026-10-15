package notijet

import (
	"strings"

	"github.com/hatami57/microjet/core/errorx"
)

// TextOrTemplate decides what an SMS-like provider sends for msg: the provider
// template when the provider supports templates and msg has one, otherwise
// msg.Text. It is what lets one Message carry both a Template and a fallback
// Text, so a Failover can move from a template provider (Kavenegar lookup) to
// a text-only one (Twilio) without rewriting the message.
//
// useTemplate is true when the template should be sent. A message with a
// Template but no Text on a provider without templates fails with an
// ErrUnsupported error.
func TextOrTemplate(provider string, msg *Message, templates bool) (useTemplate bool, err error) {
	if msg.Template != nil && templates {
		return true, nil
	}
	if strings.TrimSpace(msg.Text) != "" {
		return false, nil
	}
	if msg.Template != nil {
		return false, Unsupported(provider, "Template")
	}
	if strings.TrimSpace(msg.HTML) != "" {
		return false, Unsupported(provider, "HTML body (set Text)")
	}
	return false, Invalid(provider, "message has no Text")
}

// CheckSMS validates msg for an SMS provider: the common Validate checks, no
// email-only features, and a sendable body. templates reports whether the
// provider supports server-side templates. It returns whether to send the
// template (see TextOrTemplate).
func CheckSMS(provider string, msg *Message, templates bool) (useTemplate bool, err error) {
	if err := msg.Validate(); err != nil {
		return false, withSubject(err, provider)
	}
	if err := rejectEmailFeatures(provider, msg); err != nil {
		return false, err
	}
	return TextOrTemplate(provider, msg, templates)
}

// CheckEmail validates msg for an email provider: the common Validate checks
// over To, CC and BCC together, and a Subject unless the provider template
// supplies one. templates reports whether the provider supports server-side
// templates; useTemplate is true when the template should be sent.
func CheckEmail(provider string, msg *Message, templates bool) (useTemplate bool, err error) {
	if msg == nil {
		return false, Invalid(provider, "message is nil")
	}
	if len(msg.Recipients())+len(CleanList(msg.CC))+len(CleanList(msg.BCC)) == 0 {
		return false, Invalid(provider, "message has no recipient")
	}
	if msg.Template != nil {
		if !templates {
			if strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.HTML) == "" {
				return false, Unsupported(provider, "Template")
			}
		} else {
			if strings.TrimSpace(msg.Template.ID) == "" {
				return false, Invalid(provider, "Template.ID is required")
			}
			return true, nil
		}
	}
	if strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.HTML) == "" {
		return false, Invalid(provider, "email has no Text or HTML body")
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return false, Invalid(provider, "email has no Subject")
	}
	for _, a := range msg.Attachments {
		if strings.TrimSpace(a.Filename) == "" && a.ContentID == "" {
			return false, Invalid(provider, "attachment has no Filename")
		}
	}
	return false, nil
}

// CheckChat validates msg for a chat or webhook provider: the common checks
// minus recipients when needsRecipient is false (an incoming webhook posts to
// one fixed channel), no email-only features and no template.
func CheckChat(provider string, msg *Message, needsRecipient bool) error {
	if msg == nil {
		return Invalid(provider, "message is nil")
	}
	if needsRecipient && len(msg.Recipients()) == 0 {
		return Invalid(provider, "message has no recipient")
	}
	if err := rejectEmailFeatures(provider, msg); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.HTML) == "" {
		if msg.Template != nil {
			return Unsupported(provider, "Template")
		}
		return Invalid(provider, "message has no Text")
	}
	return nil
}

// Check validates msg for a provider of channel ch that accepts every feature
// its channel allows, templates included: CheckSMS for SMS, CheckEmail for
// email, and the common Validate checks for any other channel. It is what
// test and development providers (memory, logger) use to behave like a real
// provider of their channel.
func Check(provider string, ch Channel, msg *Message) error {
	var err error
	switch ch {
	case ChannelSMS:
		_, err = CheckSMS(provider, msg, true)
	case ChannelEmail:
		_, err = CheckEmail(provider, msg, true)
	default:
		err = withSubject(msg.Validate(), provider)
	}
	return err
}

func rejectEmailFeatures(provider string, msg *Message) error {
	switch {
	case len(msg.Attachments) > 0:
		return Unsupported(provider, "Attachments")
	case len(CleanList(msg.CC)) > 0 || len(CleanList(msg.BCC)) > 0:
		return Unsupported(provider, "CC/BCC")
	}
	return nil
}

// withSubject re-labels a validation error raised by Message.Validate (subject
// "message") with the provider name, so every error a provider returns carries
// the provider as its subject.
func withSubject(err error, provider string) error {
	if err == nil {
		return nil
	}
	if e := errorx.GetError(err); e != nil {
		return e.WithSubject(provider)
	}
	return err
}
