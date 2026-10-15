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
- [x] **1. Core** — `Message`, `Result`, `Provider`, channels, validation,
  errors, `internal/httpc` (shared HTTP helper), `internal/mimex` (MIME builder
  shared by SMTP, Gmail API and SES raw), `phone` package. Tests.
- [x] **2. Notifier and decorators** — `Notifier`, `Failover`, `Retry`, hooks,
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
- Step 1 — core done:
  - `notijet.go`: `Provider`, `Channel`, `Message` (+ `Validate`, `Clone`,
    `Recipients`, `HasBody`), `Template`/`Param` (`NewTemplate`, `Get`,
    `Values`, `Map`), `Attachment`, `Result`/`Receipt`, `DefaultHTTPClient`,
    `CleanList`.
  - `errors.go`: sentinels (`ErrUnauthorized`, `ErrInsufficientCredit`,
    `ErrInvalidRecipient`, `ErrInvalidSender`, `ErrTemplate`, `ErrRateLimited`,
    `ErrUnsupported`, `ErrNoProvider`); constructors `Invalid`,
    `InvalidRecipient`, `InvalidSender`, `Unsupported` (400), `Rejected` (409),
    `Throttled`, `Fault`, `FaultCode` (500); `IsRetryable`, `ProviderCode`. The
    provider's own code is the `providerCode` param.
  - `check.go`: `CheckSMS`, `CheckEmail`, `CheckChat`, `TextOrTemplate`.
    Convention: a message may carry both `Template` and fallback `Text`;
    template-capable providers send the template, others send `Text`, so a
    `Failover` across both kinds works.
  - `internal/httpc`: `Client.Do` returns 2xx/4xx replies for the provider to
    interpret; transport errors / 5xx → `Fault`, 429 → `Throttled`; URLs are
    stripped from transport errors (API keys live in some providers' paths).
    `Decode`, `Unexpected`, `JSON`/`Form`/`NewRequest` builders, `BasicAuth`.
  - `internal/mimex`: `Build(provider, msg, Options{From, IncludeBCC, Date,
    MessageIDDomain})` → bare `From`, envelope `Recipients`, `MessageID`,
    CRLF `Bytes`; mixed/related/alternative structure, QP text, wrapped base64
    attachments, header-injection and reserved-header checks; `ParseAddress`,
    `ParseAddressList`.
  - `phone`: `EnglishDigits`, `Clean`, `IranMobile`, `IsIranMobile`, `E164`,
    `International`, `IranLocalOrInternational`.
  - `go vet` and `go test -race ./...` pass.
- Step 2 — notifier and decorators done:
  - `notifier.go`: `NewNotifier(WithLogger)`, `Register(name, p)` (empty name
    = `p.Name()`; first provider per channel becomes the default),
    `MustRegister`, `SetDefault(name)`, `AddHooks`, `Provider`, `Default`,
    `Names`, `Send(ctx, channel, msg)`, `SendVia(ctx, name, msg)`. Logs
    successes at Debug and failures at Warn with provider/channel/recipient
    count/duration only — never recipients or content. Unknown name/channel →
    400 matching `ErrNoProvider`.
  - `hooks.go`: `Hooks{BeforeSend, AfterSend}`, `SendEvent`, `WithHooks(p, h)`.
    BeforeSend adjusts a clone of the message and can abort; AfterSend runs on
    every outcome. A provider returning `(nil, nil)` yields a `Result` naming it.
    (Plain hooks instead of a separate middleware type: decorators are just
    `func(Provider) Provider` values the caller composes.)
  - `failover.go`: `Failover(providers...)` tries in order, moves on after any
    error, stops when ctx is done; `Result.Provider` names the winner. When more
    than one fails it returns `*FailoverError{Errors}`, which unwraps to the
    first retryable error (else the last) so `IsRetryable`/category reflect it.
  - `retry.go`: `Retry(p, RetryAttempts, RetryBackoff, RetryIf)`; defaults 3
    attempts, 1s doubling to 30s, equal jitter (½–1× the delay), stops on ctx.
  - `memory`: records clones, validates per channel (CheckSMS/CheckEmail/
    Validate), receipts `<name>-N`; `FailNext(errs...)` (nil = success slot),
    `FailWith(fn)`, `Messages`, `Last`, `LastTo`, `Len`, `Calls`, `Reset`.
  - `logger`: logs the full message (content and recipients — dev only) at a
    configurable level and returns a random message ID.
  - `go vet` and `go test -race ./...` pass.

## Next up: step 3

Iranian SMS providers `sms/kavenegar`, `sms/smsir`, `sms/melipayamak`,
`sms/ghasedak`, `sms/ippanel`, each with `New(credentials, opts...)`,
`WithSender`, `WithHTTPClient`, `WithBaseURL` (for tests), text + template
sending via `notijet.CheckSMS`, phone normalization through `phone`, error-code
mapping to the notijet sentinels per the API notes below, and `httptest`-based
tests.

## Provider API notes (verified during step 0)

- Kavenegar: `https://api.kavenegar.com/v1/{API-KEY}/sms/send.json` (receptor
  comma-separated ≤200, message, sender, localid) and `verify/lookup.json`
  (receptor, token, token2, token3, token10, token20, template, type=sms|call).
  Reply `{"return":{"status":200,"message":""},"entries":[{"messageid","status",
  "statustext","receptor","cost"}]}`; errors use HTTP status = return.status:
  401/403 credentials, 411 receptor, 412 sender, 418 credit, 424/431/432
  template, 409 server busy (retry), 451 rate limit.
- SMS.ir: base `https://api.sms.ir`, header `X-API-KEY`; `POST /v1/send/bulk`
  `{lineNumber, messageText, mobiles[], sendDateTime?}` → `data{packId,
  messageIds[], cost}`; `POST /v1/send/verify` `{mobile, templateId(int),
  parameters:[{name,value}]}` → `data{messageId, cost}`. Envelope
  `{status, message, data}`, status 1 = success.
- Melipayamak REST: `https://rest.payamak-panel.com/api/SendSMS/SendSMS` form
  (username, password, to, from, text, isFlash) and `.../BaseServiceNumber`
  (username, password, to, bodyId, text = args joined by `;`). Reply
  `{"Value","RetStatus","StrRetStatus"}`; RetStatus 1 = ok and Value = recId
  (long); short/negative Value is an error code (0 credentials, 2 credit,
  5 sender, 7 filtered words, 10 inactive, 18 recipient, -4 bodyId, ...).
- Ghasedak: base `https://gateway.ghasedak.me/rest/api/v1/WebService`, header
  `ApiKey`; `POST /SendSingleSMS` `{sendDate, lineNumber, receptor, message,
  clientReferenceId, udh}`; `POST /SendOtpSMS` `{sendDate, receptors:[{mobile,
  clientReferenceId}], templateName, inputs:[{param, value}], udh, isVoice}`.
  Envelope `{isSuccess, statusCode, message, data}`; single data has
  `messageId, receptor, cost`; OTP data `{items:[...], totalCost}`.
- IPPanel (FarazSMS etc.): base `https://api2.ippanel.com/api/v1`, header
  `Apikey`; `POST /sms/send/webservice/single` `{sender, recipient[], message,
  description}`; `POST /sms/pattern/normal/send` `{code, sender, recipient,
  variable{}}`. Envelope `{status, code, error_message, data{message_id}}`.
