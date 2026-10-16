# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-16

### Added

- **`Queue.Cancel(ctx, id)`** stops a queued message that has not been sent
  yet — waiting for its first attempt (an `At` message) or for a retry — and
  keeps it with the new status `cancelled` (pruned by `Retention`). It joins
  the transaction in the context like `Enqueue`, so a cancellation commits with
  the change that voids the message. Messages being sent, and finished ones,
  are not cancelled.
- **Joining gorm transactions**: `Enqueue` and `Cancel` join a transaction
  opened directly with gorm once it is attached with microjet's
  `gormx.WithTx(ctx, tx)` (microjet v0.42.0).
- **`config/builtin/aws`** (`sns`, `ses`), **`config/builtin/google`**
  (`gmail`, `fcm`) and **`config/builtin/all`** (every type), each with a
  `Register` (or `Registry`) function and exported factories.
  `builtin.HTTPClient(settings)` gives factories outside the package the same
  `timeout` handling.
- **Kavenegar token checks**: lookup template values are checked against
  Kavenegar's rules before any request — at most 100 characters, no newline or
  underscore, no space in `token`/`token2`/`token3`, at most 5 spaces in
  `token10` and 8 in `token20` — and a voice call needs a digits-only `token`.
  These fail as `ErrTemplate` (400) instead of a provider error 431.
  `kavenegar.WithTokenSpaceReplacer(r)` (config setting
  `token_space_replacer`) substitutes spaces in the tokens that allow none.

### Changed

- **`builtin.Registry()` no longer includes `sns`, `ses`, `gmail` and `fcm`**,
  so apps that use none of them no longer link the AWS SDK and OAuth2 (a
  program using only `builtin.Registry()` shrinks from 9.4 MB to 4.1 MB,
  stripped). Register them with `aws.Register` and `google.Register`, or use
  `all.Registry()`. `builtin.GmailScope` and `builtin.FCMScope` moved to the
  `google` package. (breaking)
- **`queue.Store` has a new method, `Cancel`**; custom stores must implement
  it. (breaking)
- `email/resend` no longer depends on the AWS-specific internal helpers.
- Requires microjet v0.42.0.

## [0.1.0] - 2026-10-15

First release.

### Added

- **Core** (`notijet`): `Provider` interface, channels, `Message` (text, HTML,
  provider templates with fallback text, attachments, headers, data, metadata),
  `Result`/`Receipt`, validation helpers (`CheckSMS`, `CheckEmail`,
  `CheckChat`, `Check`), `SendBatches`.
- **Errors** on microjet `errorx` with sentinels (`ErrUnauthorized`,
  `ErrInsufficientCredit`, `ErrInvalidRecipient`, `ErrInvalidSender`,
  `ErrTemplate`, `ErrRateLimited`, `ErrUnsupported`, `ErrNoProvider`),
  `IsRetryable`, `ProviderCode`, `PartialError`, `FailoverError`.
- **Composition**: `Notifier` (named providers, channel defaults, hooks,
  logging), `Failover`, `Retry`, `WithHooks`.
- **SMS providers**: Kavenegar, SMS.ir, Melipayamak, Ghasedak, IPPanel,
  Twilio, Vonage, Plivo, Infobip, Amazon SNS.
- **Email providers**: SMTP (with presets and XOAUTH2), Gmail API, Amazon SES,
  SendGrid, Mailgun, Postmark, Resend, Brevo.
- **Chat, push and webhook providers**: Telegram, Bale, Slack, Discord,
  Firebase Cloud Messaging, signed webhooks with a receiver-side `Verifier`.
- **Development providers**: `memory` (records messages, scriptable failures)
  and `logger`.
- **`phone`**: Persian/Arabic digits, Iranian mobile and E.164 normalization.
- **`tmpl`**: local templates (subject, text, HTML) from any `fs.FS`, locale
  variants, partials, and `Decorate` to render `Message.Template` locally.
- **`queue`**: persistent delivery queue on gormx — transactional enqueue,
  lease-based workers safe across replicas, backoff, quarantine, retention.
- **`config`** and **`config/builtin`**: providers from configuration, with
  failover/retry composition and secret references.
- **`module`**: microjet host module (Notifier, queue and worker, config).
- **`providertest`**: conformance suite for HTTP providers.
- Examples: quickstart, failover, templates, queue, microjet host, webhook
  receiver.

[Unreleased]: https://github.com/hatami57/notijet/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/hatami57/notijet/releases/tag/v0.1.0
