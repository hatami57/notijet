# notijet — implementation plan

notijet is a multi-provider notification SDK for Go, built on
[microjet](../microjet) the same way [payjet](../payjet) and [bojet](../bojet)
are. Every provider — an SMS gateway, an email service, a chat bot, a push
service — implements one small interface, so applications can swap providers,
fail over between them, and add new platforms later without touching call
sites.

This file is the working plan. Each step ends with its tests passing, this file
updated, and a git commit.

## Design

### Core types (package `notijet`)

```go
type Provider interface {
    Name() string                                              // "kavenegar", "smtp", ...
    Channel() Channel                                          // sms, email, chat, push, webhook
    Send(ctx context.Context, msg *Message) (*Result, error)
}
```

- `Message` — one notification: `To`, `From`, `Subject`, `Text`, `HTML`, a
  provider-side `Template` (OTP/pattern/lookup messages, ordered named params),
  email extras (`CC`, `BCC`, `ReplyTo`, `Attachments`, `Headers`), `Data` (push /
  chat payload) and `Metadata` (tags). Providers use the fields their channel
  understands and reject the ones they cannot honour (`ErrUnsupported`) instead
  of silently dropping them.
- `Result` — one `Receipt` per recipient: provider message ID, status, cost.
- Errors are microjet `errorx.Error` values, like payjet: `Invalid` (400, bad
  input), `Rejected` (409, provider refused: credit, credentials, sender, ...),
  `Fault` (500, transport / 5xx / unreadable reply) and `Throttled` (rate limit).
  Sentinels (`ErrUnauthorized`, `ErrInsufficientCredit`, `ErrInvalidRecipient`,
  `ErrInvalidSender`, `ErrRateLimited`, `ErrUnsupported`, ...) are carried as
  `Inner`, so `errors.Is` works. `IsRetryable(err)` tells a retry loop whether
  sending again can help.

### Generic building blocks

- `Notifier` — registry of named providers, default provider per channel,
  middleware/hooks, structured logging.
- Decorators that are themselves `Provider`s: `Failover` (try the next provider
  when one fails), `Retry` (backoff on retryable errors), `Hooks`.
- `memory` provider (captures messages, for tests) and `logger` provider (prints
  through slog, for development) — the equivalent of payjet's virtual gateway.
- `phone` package: Persian/Arabic digit conversion, Iranian mobile and E.164
  normalization used by SMS providers.
- Local templates: render subject/text/HTML from `text/template` and
  `html/template` sources (embeddable with `embed.FS`).

### microjet integration

- `errorx` for every error; categories map to HTTP statuses through microjet's
  middleware.
- `notijet.Module()` registers the `Notifier`, a gormx-backed `Store` and a
  delivery `Queue` with the host. Providers can be built in code or from TOML
  config (`[notijet.providers.<name>] type = "kavenegar"`) through a factory
  registry that each provider package fills in `init()`.
- `Queue` persists messages (joining the caller's gormx transaction, like
  microjet's outbox) and a host worker delivers them with retry/backoff,
  quarantining permanent failures.
- `email/ses` reuses microjet's `aws` module client.

### Providers

| Channel | Package | Platform |
| --- | --- | --- |
| SMS (Iran) | `sms/kavenegar` | Kavenegar — send + verify/lookup |
| SMS (Iran) | `sms/smsir` | SMS.ir v1 — bulk + verify |
| SMS (Iran) | `sms/melipayamak` | Melipayamak (Payamak Panel REST) — send + BaseServiceNumber |
| SMS (Iran) | `sms/ghasedak` | Ghasedak gateway v1 — single + OTP |
| SMS (Iran) | `sms/ippanel` | IPPanel (FarazSMS & resellers) — single + pattern |
| SMS | `sms/twilio` | Twilio Messaging |
| SMS | `sms/vonage` | Vonage (Nexmo) SMS API |
| SMS | `sms/plivo` | Plivo |
| SMS | `sms/infobip` | Infobip |
| SMS | `sms/sns` | AWS SNS |
| Email | `email/smtp` | Any SMTP server; presets for Gmail, Outlook/Office 365, Yahoo, Zoho, iCloud, Yandex |
| Email | `email/gmail` | Gmail API (OAuth2 HTTP client) and Gmail SMTP |
| Email | `email/ses` | AWS SES v2 (simple and raw MIME) |
| Email | `email/sendgrid` | SendGrid v3 |
| Email | `email/mailgun` | Mailgun (US/EU) |
| Email | `email/postmark` | Postmark |
| Email | `email/resend` | Resend |
| Email | `email/brevo` | Brevo (Sendinblue) |
| Chat | `chat/telegram` | Telegram Bot API |
| Chat | `chat/bale` | Bale messenger (Telegram-compatible bot API) |
| Chat | `chat/slack` | Slack incoming webhooks |
| Chat | `chat/discord` | Discord webhooks |
| Push | `push/fcm` | Firebase Cloud Messaging HTTP v1 |
| Webhook | `webhook` | Generic signed JSON webhook |

## Steps

- [x] **0. Plan and scaffold** — git repo, `go.mod` against `../microjet`,
  `.gitignore`, this plan.
- [ ] **1. Core** — `Message`, `Result`, `Provider`, channels, validation,
  errors, `internal/httpc` (shared HTTP helper), `internal/mime` (MIME builder
  shared by SMTP, Gmail API and SES raw), `phone` package. Tests.
- [ ] **2. Notifier and decorators** — `Notifier`, `Failover`, `Retry`, hooks,
  `memory` and `logger` providers. Tests.
- [ ] **3. Iranian SMS providers** — kavenegar, smsir, melipayamak, ghasedak,
  ippanel. Tests against `httptest` servers.
- [ ] **4. International SMS providers** — twilio, vonage, plivo, infobip, sns.
  Tests.
- [ ] **5. SMTP email** — `email/smtp` with STARTTLS / implicit TLS, PLAIN and
  LOGIN auth, presets. Tests against an in-process SMTP server.
- [ ] **6. Email API providers** — gmail, ses, sendgrid, mailgun, postmark,
  resend, brevo. Tests.
- [ ] **7. Chat, push and webhook providers** — telegram, bale, slack, discord,
  fcm, webhook. Tests.
- [ ] **8. Local templates** — render messages from template sets. Tests.
- [ ] **9. microjet module, persistence and queue** — `Store` (gormx),
  `Queue` + worker, `Module()`, config-driven provider factories. Tests on
  SQLite.
- [ ] **10. Examples and docs** — focused runnable examples, a microjet host
  example, README, CHANGELOG.
- [ ] **11. Final review** — vet, race tests, staticcheck, consistency pass.

## Progress log

- Step 0 — repository created; plan written. Provider APIs checked against
  official docs/SDKs (Kavenegar REST docs, Melipayamak docs, IPPanel Go SDK,
  Ghasedak PHP SDK, SMS.ir TypeScript SDK).
