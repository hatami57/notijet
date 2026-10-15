# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
