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
- [x] **3. Iranian SMS providers** — kavenegar, smsir, melipayamak, ghasedak,
  ippanel. Tests against `httptest` servers.
- [x] **4. International SMS providers** — twilio, vonage, plivo, infobip, sns.
  Tests.
- [x] **5. SMTP email** — `email/smtp` with STARTTLS / implicit TLS, PLAIN and
  LOGIN auth, presets. Tests against an in-process SMTP server.
- [x] **6. Email API providers** — gmail, ses, sendgrid, mailgun, postmark,
  resend, brevo. Tests.
- [x] **7. Chat, push and webhook providers** — telegram, bale, slack, discord,
  fcm, webhook. Tests.
- [x] **8. Local templates** — render messages from template sets. Tests.
- [x] **9. microjet module, persistence and queue** — `queue` (Store,
  gormx DBStore, lease-based worker), `module` (host wiring). Tests on SQLite.
- [x] **10. Config-driven provider factories** — build providers from TOML /
  map config (`type = "kavenegar"`), registry, `module.WithConfig`. Tests.
- [ ] **11. Examples and docs** — focused runnable examples, a microjet host
  example, README, CHANGELOG.
- [ ] **12. Final review** — vet, race tests, staticcheck, consistency pass.

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

- Step 3 — Iranian SMS providers done:
  - Core additions: `SendBatches` (chunked sending loop) and `PartialError`
    (some recipients reached before a failure; never retryable, stops a
    `Failover`, and `FailoverError` unwraps to it), `InvalidTemplate` (400 +
    `ErrTemplate`). `internal/smsutil` (`Recipients` normalize + dedupe,
    `Sender`, `Flag`), `internal/flexjson.String`.
  - Every SMS provider: `New(credentials, opts...)` with `WithSender`,
    `WithHTTPClient`, `WithBaseURL`; exported `Name` and `DefaultBaseURL`;
    validation before any request; template when the message has one, else
    text (via `CheckSMS`); provider codes → sentinels, kept as `providerCode`.
  - `sms/kavenegar`: send.json in 200-receptor batches (API key path-escaped),
    lookup per recipient; params → token/token2/token3/token10/token20 by name
    or position; `Data["type"]="call"`; numeric `Message.ID` → localid.
  - `sms/smsir`: bulk in 100-mobile batches (numeric line required), verify
    per recipient (numeric template ID).
  - `sms/melipayamak`: SendSMS in 100-recipient batches, BaseServiceNumber per
    recipient (numeric bodyId, values joined by `;`); Value in -10..35 is an
    error code, larger values are recIds; `Data["flash"]`.
  - `sms/ghasedak`: SendSingleSMS per recipient, SendOtpSMS in 100-receptor
    batches; generated clientReferenceIds; `Data["voice"]`; a 429 whose body
    is a Ghasedak status means "template param not found", not a rate limit.
  - `sms/ippanel`: E.164 recipients, webservice in 100-recipient batches (one
    message ID per request), pattern per recipient; errors from
    `error_message` or `data.error` (string or field map).
  - `go vet` and `go test -race ./...` pass.

- Step 4 — international SMS providers done:
  - All take `WithDefaultCountry(code)` for national-form numbers (rejected
    without it), send `Text` only (`CheckSMS(..., false)`; a Template needs a
    fallback Text), and report receipts with E.164 `To`.
  - `sms/twilio`: Messages.json per recipient, basic auth with the auth token
    or `WithAPIKey(sid, secret)`; an `MG…` sender becomes
    MessagingServiceSid; `WithStatusCallback`; a `failed`/`undelivered` reply
    is an error; error codes 20003/20005/20008 auth, 21211/21214/21217/21265/
    21408/21608/21610/21612/21614 recipient, 21212/21603/21606/21659/21660
    sender, 20429/14107 rate limit. Error replies have a numeric `status`.
  - `sms/vonage`: /sms/json per recipient; `type=unicode` when the text is not
    GSM-7 (`smsutil.IsGSM7`); `client-ref` from Message.ID (≤100 chars);
    `WithCallback`; status 1 throttled, 4/8/11/14/32 auth, 5 fault,
    6/7/29/33 recipient, 9 credit, 15 sender; any failed part fails the send.
  - `sms/plivo`: bulk `dst` joined by `<` (≤1000), UUID sender →
    `powerpack_uuid`; `invalid_number` entries get receipts with status
    "invalid"; text errors classified by wording.
  - `sms/infobip`: account base URL required in `New`; `Authorization: App`;
    /sms/2/text/advanced (≤1000 destinations), Message.ID → callbackData;
    partial REJECTED destinations are receipts, all rejected → error by
    status name (CREDIT, SENDER, DESTINATION/PREFIX/NETWORK, FLOODING).
  - `sms/sns`: takes any `API` (`*sns.Client` satisfies it) — credentials and
    region stay with the caller's AWS config; Publish per recipient with
    SMSType (Transactional default), SenderID or OriginationNumber (`+…`);
    smithy API error codes mapped; server faults retryable.
  - Dependencies: aws-sdk-go-v2 (sns), smithy-go. Note: this machine sets
    `GOPRIVATE=github.com/*`, so fetch public modules with
    `GOPRIVATE='github.com/hatami57/*' GONOSUMDB='github.com/hatami57/*'`.
  - `go vet` and `go test -race ./...` pass.

- Step 5 — SMTP email done, plus a test-quality pass over steps 2–4:
  - `email/smtp`: `New(host, port, opts...)` (port 0 → 587, 465 → implicit
    TLS), `Security` = `StartTLS` (default; fails when not offered) |
    `ImplicitTLS` | `NoTLS`; `Preset{Host, Port, Security}` with `.New(...)`
    for Gmail, Outlook, Office365, Yahoo, Zoho, ICloud, Yandex. Options:
    `WithAuth` (PLAIN, else LOGIN — picked from the server's AUTH list),
    `WithOAuth2(user, tokenFunc)` (XOAUTH2), `WithSecurity`, `WithTLSConfig`,
    `WithFrom`, `WithLocalName`, `WithMessageIDDomain`, `WithTimeout`,
    `WithDialer`. Built on net/smtp (imported as `netsmtp`) with ctx-aware
    dialing, conn deadlines and cancellation (`context.AfterFunc`).
    Credentials are never sent unencrypted to a non-localhost server. Receipt
    MessageID = the Message-ID header; partially refused RCPTs are delivered
    to the rest with "rejected: <code> <text>" receipts, all refused → error.
    Reply mapping: 4xx → retryable fault; 530/534/535/538 or any auth-stage
    5xx → `ErrUnauthorized`; MAIL 550/553/501/555 → `ErrInvalidSender`; RCPT
    550/551/553/501 → `ErrInvalidRecipient`; untrusted certificate →
    permanent; dial/I/O errors stripped of socket addresses; a deadline that
    passed reports `context.DeadlineExceeded`.
  - Tests use an in-process fake SMTP server (`fake_test.go`: EHLO,
    STARTTLS, implicit TLS, AUTH PLAIN/LOGIN/XOAUTH2, per-recipient replies,
    DATA replies, stalling) with a generated self-signed certificate.
  - `providertest.RunHTTP`: exported conformance suite every HTTP provider
    runs (identity, validation before any request, 5xx/429/unreadable
    reply/transport failure categories, custom HTTP client used, cancelled
    context not retryable, secret never in errors). All nine HTTP SMS
    providers run it.
  - `notijet.Check(provider, channel, msg)` replaces the duplicated channel
    checks in `memory` and `logger`.
  - Coverage: every package 93–100% except `providertest` (82%, its own
    failure-reporting branches); the rest are unreachable JSON-encoding error
    branches.
  - `go vet` and `go test -race ./...` pass.

## Testing policy (from the user: "complete tests and no mistake, keep the project generic")

Every provider ships with: happy paths for each send mode, request shape
assertions, every error-code mapping (sentinel, providerCode, retryability),
validation-before-network, unexpected replies, and `providertest.RunHTTP`
when it speaks HTTP. Verify API details against official docs before coding.
Keep core types provider-neutral.

- Step 6 — email API providers done:
  - Shared: `internal/emailutil` (`Prepare` = `CheckEmail` + address parsing
    + `mimex.CheckHeaders`, `Display` with UTF-8 names, `Emails`,
    `Receipt`), `internal/awsutil` (`Classify` for non-API / auth / throttle
    / server-fault AWS errors, `ValidTag`; `sms/sns` refactored onto it).
    `mimex.ContentType` and `mimex.CheckHeaders` exported; fixed attachments
    whose type has parameters (`.txt` → empty Content-Type) and empty header
    names.
  - `email/sendgrid`: v3 mail/send, Bearer; one personalization with
    addresses deduplicated across To/CC/BCC; needs a To; requires HTTP 202
    (any other 2xx is unexpected); dynamic templates (`template_id` +
    `dynamic_template_data`); Metadata → custom_args; inline cid
    attachments; `EUBaseURL`. Errors mapped from message/field (credits,
    sender identity, template_id, personalizations email).
  - `email/mailgun`: multipart form to /v3/{domain}/messages, basic auth
    `api:key`, `EUBaseURL`; template + `t:variables`; Metadata → `v:` vars;
    headers → `h:`; inline parts named by ContentID; 401/403 auth, 402
    credit, 404/'from'/domain sender, 'to'/'cc'/'bcc' recipient, template.
  - `email/postmark`: /email and /email/withTemplate (numeric ID →
    TemplateId, else TemplateAlias); ≤50 recipients; `WithMessageStream`;
    ErrorCode mapping 10/413/1480 auth, 400/401 sender, 405 credit, 406
    recipient, 1101/1109/1122/1123 template, 100/101 fault.
  - `email/resend`: /emails, ≤50 To; template {id, variables}; Metadata →
    tags (validated); Message.ID → Idempotency-Key (≤256); 429 body read so
    daily/monthly quota is permanent while rate_limit_exceeded is retryable;
    concurrent_idempotent_requests retryable.
  - `email/brevo`: /v3/smtp/email, `api-key`; numeric templateId + params;
    single Reply-To; inline attachments unsupported; Metadata →
    `X-Mailin-custom` ("k:v|k:v", validated, conflicts with an explicit
    header rejected); code mapping unauthorized/permission_denied/
    account_under_validation, not_enough_credits, document_not_found
    (template), invalid/missing_parameter by wording.
  - `email/ses`: sesv2 `SendEmail` behind `API`; Simple content with
    attachments (inline → INLINE + ContentId) and headers; Template content
    with JSON TemplateData; non-ASCII names as encoded-words, bare addresses
    otherwise; Metadata → EmailTags; `WithConfigurationSet`; codes
    MailFromDomainNotVerified → sender, AccountSuspended/SendingPaused →
    auth, NotFound/"template" with a template → ErrTemplate.
  - `email/gmail`: media upload of the mimex message (Bcc header included)
    to /upload/gmail/v1/users/{user}/messages/send; auth via an OAuth2
    `WithHTTPClient` or `WithToken`; `WithUser`; Google error reasons →
    rate limits retryable, daily quota permanent, 401/403 auth, Invalid
    To/From headers.
  - Every HTTP email provider runs `providertest.RunHTTP`; coverage 97–100%.
  - `go vet` and `go test -race ./...` pass.

- Step 7 — chat, push and webhook providers done:
  - `internal/botapi`: sendMessage client for Telegram-compatible bot APIs —
    one call per chat (or `DefaultChat`), Subject as first line, 4096-char
    limit (runes), HTML → parse_mode HTML where supported,
    `Data["parse_mode"]`, silent / no-link-preview; errors: 401/404 auth,
    403 (blocked/kicked) and "chat not found" → recipient, 429 throttle, 5xx
    fault. Token in the path never reaches errors.
  - `chat/telegram` (`WithChatID`, `WithSilent`, `WithoutLinkPreview`,
    `WithBaseURL` for a local Bot API server) and `chat/bale`
    (tapi.bale.ai; Markdown only, so HTML and parse_mode are unsupported).
  - `internal/chatutil` (`CheckWebhook`: no recipients, Text required;
    `Text` with Subject line). `chat/slack`: incoming webhook, requires body
    "ok", `Data["blocks"]` JSON array, plain-text error codes mapped
    (no_service/invalid_token… auth; channel_not_found/archived… recipient).
    `chat/discord`: webhook with `?wait=true` (keeps existing query),
    ≤2000 chars, mentions ping no one unless `WithMentions`, `WithUsername`,
    `WithAvatarURL`, `Data["embeds"]`; 10015/50027/401/403/404 → auth.
  - `push/fcm`: HTTP v1 per recipient (token, "topic:x" or "/topics/x",
    "condition:…"); Subject/Text → notification, Data → data; data-only
    messages allowed; OAuth2 client or `WithToken`; stale tokens
    (UNREGISTERED, NOT_FOUND, INVALID_ARGUMENT about the token) become
    "invalid: <code>" receipts and only fail the send when every recipient
    is invalid; other failures after a success → `PartialError`;
    SENDER_ID_MISMATCH/THIRD_PARTY_AUTH_ERROR/401/403 auth, QUOTA_EXCEEDED
    throttle.
  - `webhook`: JSON `Payload` of every message field (`NewPayload`,
    `Payload.Message` round-trip), optional HMAC-SHA256 signature
    (`X-Notijet-Timestamp`, `X-Notijet-Signature: sha256=…` over
    "ts.body"), `WithHeader`, `WithChannel`; any 2xx succeeds (JSON `id` →
    MessageID); 401/403 auth, 408 retryable. Receiver side: `Sign`,
    `Verifier{Secret, Tolerance, Now}.Verify/VerifyBody` (constant-time
    compare, ±5 min window, empty secret refused).
  - `providertest.HTTPCase` gained `RecipientOptional` and `AnySuccessBody`
    for fixed-destination and generic endpoints.
  - Coverage 94–100% (providertest 82%); `go vet` and `go test -race ./...`
    pass.

- Step 8 — local templates done (package `tmpl`, provider-neutral):
  - `Set` from `ParseFS(fsys, dir, opts...)` (walks dir; `.subject`, `.txt`,
    `.html`; name = path relative to dir without extension) or
    `New(opts...)` + `Add(name, part, src)`. Subject and text parts use
    text/template, HTML uses html/template. Files whose base name starts
    with `_` are partials, included as `{{template "_footer.html" .}}`.
    Options `WithFuncs`, `MissingKeyZero` (missing keys are errors by
    default). Adding after the first render is an error.
  - `Render`, `RenderLocale(name, locale, data)` (tries name.fa-IR,
    name.fa, name; a whole variant, never mixed), `Has(name, locale)`,
    `Apply(msg, name, locale, data)` (sets only the parts the template has).
    Subject collapsed to one line; parts trimmed. Errors are 400 with
    `ErrTemplate`.
  - `Decorate(p, set, localeFunc)`: renders a Message.Template the set knows
    (params as data) into a clone and clears Template; unknown templates
    pass through to the provider.
  - Coverage 99%; `go vet` and `go test -race ./...` pass.

- Step 9 — persistence, queue and microjet module done:
  - Design: core `notijet` stays free of gorm/host; `queue` (gormx) and
    `module` (host) are separate packages. Config factories moved to a new
    step 10 (they need a config constructor in every provider).
  - `queue.Record` (table `notijet_messages`): ID (Message.ID or generated),
    Channel, Provider (Via name), JSON Message, Status
    queued/sending/sent/partial/failed, Attempts, NextAttemptAt,
    LockedUntil, ClaimToken, SentVia, JSON Receipts, LastError, ErrorCode,
    timestamps; `Decode`, `DecodeReceipts`, `Done`.
  - `queue.Store` interface + `DBStore` over gormx tables (Insert joins the
    caller's `RunTx`; existing ID → no-op, a concurrent duplicate →
    `ErrDuplicate`). Claim = read due IDs, then one conditional UPDATE per
    row (status/next_attempt_at or expired lease) setting a fresh
    ClaimToken, lease and attempts+1 — no row locks, replica-safe on every
    dialect incl. SQLite. Finish is conditional on the ClaimToken, so a
    worker whose lease expired cannot overwrite the new owner's outcome.
    Requeue (failed/partial → queued, attempts reset), Prune (final rows).
  - `queue.Queue`: `Enqueue(ctx, ch, msg, Via(name), At(t))` (provider must
    exist now; wakes Run), `Process` (one pass, `Workers(n)` in parallel,
    each send bounded to 90% of the lease), `Run` (interval + wake-ups,
    pass errors logged), `Get`, `Retry`. Outcomes: success → sent with
    receipts; `PartialError` → partial (never resent); retryable and
    attempts < MaxAttempts → queued with jittered exponential backoff;
    otherwise failed; an unreadable stored message fails; on shutdown the
    claim is left to expire. Options: Interval, BatchSize, Workers, Lease,
    MaxAttempts, Backoff, Retention, WithClock, WithLogger.
  - `module.New(WithProvider, WithDefault, WithHooks, WithQueue(opts...),
    WithDatabase, WithStore, WithoutWorker)`: registers the Notifier (app
    logger) and, with the queue, a `*queue.Queue` (app clock) available
    from Register on, a setup service that connects/migrates the store,
    and the "notijet-queue" worker. Accessors `Notifier`, `Queue`,
    `LookupQueue`.
  - Tests: SQLite in-memory and file DBs — transaction join/rollback,
    two replicas delivering 60 messages exactly once, lease takeover and a
    lost claim, a real concurrent duplicate insert, retention, shutdown,
    host lifecycle via `app.Start`/`Shutdown`. Coverage queue 97%, module
    99%. `go vet` and `go test -race ./...` pass.

- Step 10 — config-driven providers done:
  - `config` (no provider imports): `Spec{Name, Settings}`, `Registry`
    (case-insensitive types), `Builder{Registry, Resolve, Schemes}.Build`
    → `Built{Providers, Defaults}` + `Register(notifier)`. Reserved keys:
    type, default, retry_attempts/retry_initial/retry_max (wraps in Retry),
    and type "failover" with a providers list (members in any order,
    nested failovers, unknown names and cycles reported, a member's error
    reported once). Values "env:NAME", "file:/path" resolved built-in;
    other schemes via `Resolve`. `Settings` (Required, String, Bool, Int,
    Duration, List, OneOf, Prefixed, Errorf, Err) tracks reads so unknown
    keys (typos) are errors.
  - `config/builtin.Registry()`: factories for all 26 types (every provider
    plus logger and memory), settings documented in the package comment;
    `timeout` per HTTP provider; AWS via the default chain with region /
    static keys / endpoint; Gmail and FCM via service-account JSON
    (credentials_json or credentials_file, Gmail `user` = domain-wide
    delegation subject) or Application Default Credentials, token refresh
    on its own context. Dependencies added: golang.org/x/oauth2,
    aws-sdk-go-v2/config.
  - `module.WithConfig(registry)` reads `[notijet.providers.<name>]`
    (`module.ConfigSection`) during config loading, registers after code
    providers, then applies `WithDefault`s (which may name configured
    providers); `module.WithSecrets(secretx.Resolver, schemes...)` (default
    scheme "secret"); `module.Specs` flattens nested tables to dotted keys
    and lists to commas.
  - Tests: each HTTP factory builds from config against a recording server
    and its credential/sender reach the wire; SNS (query/XML) and SES
    (REST/JSON) against a fake endpoint with static keys; Gmail and FCM with
    a generated service-account key and fake token endpoint, plus ADC via
    GOOGLE_APPLICATION_CREDENTIALS; required-key and typo errors; module
    startup via configx.NewMapReader. Coverage config 99%, builtin 99%,
    module 99%. `go vet` and `go test -race ./...` pass.

## Next up: step 11

Examples and docs: `examples/` as separate `main` packages (in this module,
built by `go vet ./...`): quickstart (memory/logger + Notifier), sms-otp
(template with fallback text + Failover + Retry), email (SMTP preset + local
templates + attachments), queue (SQLite, enqueue in a transaction, worker),
microjet-host (module with TOML config, builtin registry, secrets), webhook
receiver (Verifier). README (overview, provider table with features,
error model, Failover/Retry/PartialError semantics, templates, queue,
config reference pointer, testing with memory/providertest), CHANGELOG
(v0.1.0), package doc review. Every example must compile and run against
the logger/memory providers or local test servers without credentials.

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

### Error codes confirmed during step 3

- SMS.ir status (sms.ir/rest-api): 1 ok, 0 server error, 10–14 key/account
  (invalid, disabled, IP-restricted, inactive, suspended), 20 rate limit,
  101 line invalid, 102 credit, 103 empty text, 104 invalid mobile, 105/106
  >100 mobiles/texts, 107–110 list/date errors, 113 template not found, 114
  param >25 chars, 115 blacklisted, 116 empty param name, 117 text not
  approved, 118 too many messages, 119 plan upgrade, 123 line not activated.
  `BulkSendModel.lineNumber` is int64 (OpenAPI at api.sms.ir/swagger).
- Ghasedak statusCode (ghasedak.me/docs): 200 ok, 400 bad input, 401 key /
  inactive account, 402 failed, 406/412 line problems, 413 too long / too
  many recipients, 416 IP mismatch, 418 credit, 419 tariff, 420 forbidden
  link, 422 bad characters, 426 plan upgrade, 428 template invalid, 429 param
  not found, 451 duplicate request, 500 error. Reply keys are PascalCase.
- IPPanel (go-rest-sdk v2): envelope `{status, code, error_message, data}`,
  header `Apikey`; pattern `recipient` is a single string, `variable` a map;
  webservice `description{summary, count_recipient}`; validation errors come
  as `data.error` (string or field → messages map).
- Melipayamak codes (from the panel's web service docs): 0 credentials, 2
  credit, 3/4 send limits, 5 sender, 6 updating, 7 filtered words, 9 public
  line, 10 inactive user, 11 not sent, 12 documents, 14 link, 15 missing
  لغو11, 16 recipient not found, 17 empty text, 35 blacklist; pattern: -1
  access disabled, -2 recipients, -3 line, -4 bodyId, -5 args mismatch, -6
  internal, -7 sender, -10 link in args.
