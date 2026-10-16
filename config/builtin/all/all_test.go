package all

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryHasEveryType(t *testing.T) {
	assert.Equal(t, []string{
		"bale", "brevo", "discord", "fcm", "ghasedak", "gmail", "infobip", "ippanel", "kavenegar",
		"logger", "mailgun", "melipayamak", "memory", "plivo", "postmark", "resend", "sendgrid",
		"ses", "slack", "smsir", "smtp", "sns", "telegram", "twilio", "vonage", "webhook",
	}, Registry().Types())
}
