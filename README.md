# notijet

A multi-provider notification SDK for Go: SMS gateways (Iranian and
international), email services, chat bots, push and webhooks behind one small
interface. Swap providers without touching call sites, fail over between them,
retry what is worth retrying, render content from local templates, and queue
messages in the same database transaction as the change they announce.

notijet builds on [microjet](https://github.com/hatami57/microjet) — errors
are `errorx` values that microjet maps to HTTP statuses, and the optional
`module` package wires everything into a microjet host app — but the core and
every provider work in any Go program.

```go
p := kavenegar.New(apiKey, kavenegar.WithSender("10004346"))
res, err := p.Send(ctx, &notijet.Message{
    To:   []string{"09121234567"},
    Text: "Your order has shipped.",
})
```

## Install

```sh
go get github.com/hatami57/notijet
```

Requires Go 1.26.

## Providers

| Channel | Package | Constructor | Provider templates | Notes |
| --- | --- | --- | --- | --- |
| SMS | `sms/kavenegar` | `kavenegar.New(apiKey, ...)` | verify/lookup (`token`…`token20`) | 200 recipients per request; voice OTP |
| SMS | `sms/smsir` | `smsir.New(apiKey, ...)` | verify (numeric template ID) | Iranian mobiles; bulk of 100 |
| SMS | `sms/melipayamak` | `melipayamak.New(user, pass, ...)` | BaseServiceNumber (`bodyId`) | Payamak Panel resellers via `WithBaseURL` |
| SMS | `sms/ghasedak` | `ghasedak.New(apiKey, ...)` | OTP templates | voice OTP |
| SMS | `sms/ippanel` | `ippanel.New(apiKey, ...)` | patterns | FarazSMS and other IPPanel panels |
| SMS | `sms/twilio` | `twilio.New(sid, token, ...)` | — | Messaging Service SIDs, API keys |
| SMS | `sms/vonage` | `vonage.New(key, secret, ...)` | — | automatic unicode for non-GSM text |
| SMS | `sms/plivo` | `plivo.New(authID, token, ...)` | — | bulk of 1000, Powerpack senders |
| SMS | `sms/infobip` | `infobip.New(baseURL, apiKey, ...)` | — | bulk of 1000 |
| SMS | `sms/sns` | `sns.New(snsClient, ...)` | — | AWS SDK v2 client |
| Email | `email/smtp` | `smtp.New(host, port, ...)`, `smtp.Gmail.New(...)` | — | STARTTLS / TLS, PLAIN / LOGIN / XOAUTH2, presets for Gmail, Outlook, Office 365, Yahoo, Zoho, iCloud, Yandex |
| Email | `email/gmail` | `gmail.New(gmail.WithHTTPClient(oauthClient))` | — | Gmail API, up to 35 MB |
| Email | `email/ses` | `ses.New(sesv2Client, ...)` | stored templates | attachments, tags, configuration sets |
| Email | `email/sendgrid` | `sendgrid.New(apiKey, ...)` | dynamic templates | EU region |
| Email | `email/mailgun` | `mailgun.New(domain, apiKey, ...)` | stored templates | EU region |
| Email | `email/postmark` | `postmark.New(serverToken, ...)` | templates by ID or alias | message streams |
| Email | `email/resend` | `resend.New(apiKey, ...)` | published templates | idempotency keys |
| Email | `email/brevo` | `brevo.New(apiKey, ...)` | templates (numeric ID) | |
| Chat | `chat/telegram` | `telegram.New(botToken, ...)` | — | HTML / MarkdownV2, silent messages |
| Chat | `chat/bale` | `bale.New(botToken, ...)` | — | Bale messenger (Telegram-compatible API) |
| Chat | `chat/slack` | `slack.New(webhookURL)` | — | incoming webhooks, Block Kit |
| Chat | `chat/discord` | `discord.New(webhookURL, ...)` | — | mentions off by default |
| Push | `push/fcm` | `fcm.New(projectID, ...)` | — | FCM HTTP v1; tokens, topics, conditions |
| Webhook | `webhook` | `webhook.New(url, ...)` | — | HMAC-SHA256 signed JSON, receiver-side `Verifier` |
| Dev | `memory` | `memory.New(...)` | all | records messages; scriptable failures |
| Dev | `logger` | `logger.New(slogLogger, ...)` | all | prints messages instead of sending |

Every provider validates a message before any network call and rejects
features it cannot honour (`ErrUnsupported`) rather than dropping them: an
attachment on SMS, HTML on a Markdown-only bot, a Template on a text-only
gateway with no fallback Text.

## Messages

```go
type Provider interface {
    Name() string
    Channel() notijet.Channel // sms, email, chat, push, webhook
    Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error)
}
```

A `Message` carries what any channel needs — `To`, `From`, `Subject`, `Text`,
`HTML`, a provider `Template`, email `CC`/`BCC`/`ReplyTo`/`Attachments`/`Headers`,
a `Data` payload (push data, provider knobs) and `Metadata` tags. Each provider
uses the fields its channel understands; recipients are normalized (Persian
digits, `+98`/`0098`/`0` prefixes, E.164 — see the `phone` package).

A message may carry both a `Template` and a fallback `Text`: providers with
server-side templates send the template, the others send the text, so one
message works across a failover chain.

The `Result` holds one `Receipt` per recipient (or one for the whole message
where the provider reports a single ID) with the provider's message ID,
status and cost.

## Sending

```go
n := notijet.NewNotifier(notijet.WithLogger(logger))
n.MustRegister("otp", notijet.Failover(
    notijet.Retry(kavenegar.New(kavKey), notijet.RetryAttempts(3)),
    twilio.New(sid, token, twilio.WithSender("+15005550006")),
))
n.MustRegister("mail", smtp.Gmail.New(smtp.WithAuth(user, appPassword)))

n.Send(ctx, notijet.ChannelSMS, msg)  // the channel's default provider
n.SendVia(ctx, "mail", msg)           // by name
```

- **`Notifier`** — named providers, a default per channel, hooks
  (`BeforeSend` may adjust a copy of the message or veto it, `AfterSend` sees
  every outcome) and structured logging that records recipient counts, never
  recipients or content (a failure's error is logged as the provider reported
  it).
- **`Failover(providers...)`** — tries each in order until one succeeds.
- **`Retry(p, ...)`** — retries retryable errors with jittered exponential
  backoff, honouring the context.
- **`WithHooks(p, hooks)`** — hooks around a single provider.

## Errors

Errors are microjet `*errorx.Error` values with a category and a sentinel:

| Category | HTTP | Meaning | Retryable |
| --- | --- | --- | --- |
| BadRequest | 400 | bad input caught before sending: no recipient, malformed number, unsupported feature | no |
| Business | 409 | the provider refused: credentials, credit, sender, recipient, template | no |
| Internal | 500 | transport failure, 5xx, unreadable reply, rate limit | yes |

```go
switch {
case errors.Is(err, notijet.ErrInsufficientCredit): // top up, or fail over
case errors.Is(err, notijet.ErrInvalidRecipient):    // fix the number/address
case notijet.IsRetryable(err):                       // try again later
}
code := notijet.ProviderCode(err) // the provider's own code, e.g. "418"
```

Sentinels: `ErrUnauthorized`, `ErrInsufficientCredit`, `ErrInvalidRecipient`,
`ErrInvalidSender`, `ErrTemplate`, `ErrRateLimited`, `ErrUnsupported`,
`ErrNoProvider`.

Two error types describe multi-step outcomes:

- **`PartialError`** — a provider that sends in several requests (per
  recipient or per batch) failed after some succeeded. It carries the receipts
  of the recipients reached, is never retryable, and stops a `Failover`:
  resending the whole message would duplicate it.
- **`FailoverError`** — every provider of a failover failed. It lists each
  error and unwraps to the one that decides retryability (a partial failure,
  else the first retryable error, else the last).

## Local templates

```go
//go:embed templates
var files embed.FS

set, _ := tmpl.ParseFS(files, "templates")
r, _ := set.RenderLocale("order-shipped", "fa-IR", data) // .subject, .txt, .html
set.Apply(msg, "order-shipped", "en", data)              // fill a Message
p := tmpl.Decorate(smtpProvider, set, localeOf)          // render msg.Template locally
```

Subjects and text use `text/template`, HTML uses `html/template`. Locale
variants (`order-shipped.fa.txt`) fall back to the base, files under
`partials/` are shared, and missing keys are errors by default.

## Queue

```go
q := queue.New(queue.NewDBStore(db), n, queue.MaxAttempts(8))
err := repo.RunTx(ctx, func(ctx context.Context) error {
    if err := orders.Create(ctx, &order); err != nil {
        return err
    }
    _, err := q.Enqueue(ctx, notijet.ChannelSMS, msg) // commits with the order
    return err
})
go q.Run(ctx)
```

Messages are stored in `notijet_messages` (any gormx database) and delivered by
workers that claim them with a lease, so several workers or replicas share one
queue safely on any database. Retryable failures back off and retry; permanent
ones and exhausted retries are kept as `failed` (`Retry` requeues one); a
partly delivered message is never resent. `At(t)` schedules, `Via(name)` picks
a provider, and a fixed `Message.ID` makes enqueueing idempotent.

## microjet module and configuration

```go
host.MustNew().WithModules(
    gormx.Module(postgres.Driver()),
    module.New(
        module.WithConfig(builtin.Registry()),  // [notijet.providers.*] from config
        module.WithSecrets(secretResolver),     // "secret:<ref>" values
        module.WithQueue(),
    ),
).MustRun()

module.Notifier(app).Send(ctx, notijet.ChannelSMS, msg)
module.Queue(app).Enqueue(ctx, notijet.ChannelEmail, msg)
```

```toml
[notijet.providers.otp]
type = "kavenegar"
api_key = "env:KAVENEGAR_API_KEY"
retry_attempts = 3

[notijet.providers.sms]
type = "failover"
providers = "otp, otp-backup"
default = true
```

`config/builtin` documents every provider type and its settings; unknown keys
are errors, so a typo cannot silently drop a setting. Outside microjet, use
`config.Builder` directly with any map of settings.

## Testing

- `memory.New()` records messages (`Last`, `LastTo`, `Messages`) and fails on
  demand (`FailNext`, `FailWith`) — wire it in place of a real provider.
- `logger.New(slog)` prints messages during development.
- `providertest.RunHTTP` is the conformance suite every built-in HTTP provider
  passes; run it against your own providers.

## Examples

| Example | Shows |
| --- | --- |
| [`quickstart`](examples/quickstart) | Notifier, channels, errors |
| [`failover`](examples/failover) | Template with fallback text, Retry + Failover across Kavenegar and Twilio (simulated) |
| [`templates`](examples/templates) | embedded templates, locales, partials, inline images |
| [`queue`](examples/queue) | enqueue in a transaction, retry with backoff (SQLite) |
| [`microjet`](examples/microjet) | host module, TOML providers, queue worker |
| [`webhook-receiver`](examples/webhook-receiver) | signed webhooks and verification |

All run without credentials: `go run ./examples/quickstart` (the microjet
example reads its config from its own directory: `cd examples/microjet && go
run .`).
