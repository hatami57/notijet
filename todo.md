# TODO

Improvements found while planning notijet's use in consulter (an Iranian
consultation-booking app on microjet: OTP login by SMS, plus booking
confirmation, cancellation and reminder notifications by SMS and email).

All done (unreleased; see CHANGELOG.md):

- [x] 1. `Queue.Cancel(ctx, id)` — cancels a queued message (first attempt or
  retry pending) as `StatusCancelled`, joining the context's transaction;
  `Store.Cancel` added to the interface, `DBStore` and the module's
  `lazyStore`.
- [x] 2. Enqueue inside an existing gorm transaction — microjet v0.42.0
  `gormx.WithTx(ctx, tx)`; notijet requires v0.42.0, tests enqueue and cancel
  inside `db.Transaction`, README and doc comments mention it.
- [x] 3. Lighter `config/builtin` — AWS types in `config/builtin/aws`, Google
  types in `config/builtin/google`, everything in `config/builtin/all`;
  exported factories and `builtin.HTTPClient`; a test guards against the SDKs
  creeping back in.
- [x] 4. Kavenegar lookup token validation — Kavenegar's rules checked before
  the request (`ErrTemplate`), plus `WithTokenSpaceReplacer` /
  `token_space_replacer`.
