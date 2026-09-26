# Pool Upstream Information

Status: approved by the account owner on 2026-09-27.

## Goal and boundaries

An administrator can opt an API-key pool-mode account into an explicitly
selected upstream information probe. The account table shows the resulting
information immediately after the upstream-declared billing rate. Accounts
without the new configuration behave exactly as before: same-account retry,
billing-rate probing, routing, and billing are unchanged.

Only `sub2api` is changed. The ChatGPT2API deployment has been verified with
a read-only `GET /api/dashboard` authenticated by an administrator key; its
response includes `accounts.active`, `total_quota`, `unlimited_quota_count`,
and `unknown_quota_count`. The administrator key is never stored in this
document or used by tests. Do not probe the production deployment again for
development verification.

## Configuration and interface

- Add a pool upstream platform selector to the API-key account create and
  edit forms: `default` (legacy/no probe), `sub2api`, `chatgpt2api`.
  Bedrock pool mode is unchanged. Display the selector and feature choices
  only when pool mode is on.
- Feature choices are multi-select: `sub2api` offers `balance`;
  `chatgpt2api` offers `account_count` and `image_quota`. All start unchecked.
  Switching platform clears incompatible choices. Disabling pool mode saves
  the default platform with no features. Only `openai` API-key accounts can
  choose `chatgpt2api`; Sub2API can be chosen for supported API-key accounts.
- Store normalized `pool_upstream_platform` and `pool_upstream_features` in
  `accounts.extra`, not credentials. Store a sanitized
  `pool_upstream_info` snapshot separately in `extra`. Validate the enums,
  combinations, type, and pool-mode prerequisite on the backend; reject
  unsupported choices rather than silently changing them. Ignore or reject
  client-provided snapshots. Reset the snapshot if the selected platform,
  features, API key, Base URL, proxy, or relevant headers change.
- Return the snapshot in the authenticated, paginated lightweight
  `/admin/accounts/upstream-billing-rates` projection and its ETag. The
  table must not call upstream services from the browser; reuse existing
  filter/page/order reconciliation. Provide a per-row admin-only manual
  refresh action for an enabled feature.

## Probe semantics

- For `sub2api`, request `GET /v1/usage` with the configured API key.
  When `mode=unrestricted` and a numeric `balance` is present, display
  wallet balance in USD. Otherwise display a numeric `remaining` as
  remaining USD quota, not wallet balance. Missing values are unknown,
  never zero by default. A response with an unexpected unit or invalid
  number is not a successful measurement.
- For `chatgpt2api`, request `GET /api/dashboard` using the administrator
  API key configured on that upstream account. Project only
  `accounts.active`, `total_quota`, `unlimited_quota_count`, and
  `unknown_quota_count`. Label the first value "normal accounts", not
  remotely confirmed available accounts; label the second "known image
  quota", not money. Missing or invalid fields do not become zeros.
- Normalize the configured API Base URL into a service root, preserving a
  reverse-proxy path prefix while removing a trailing API version (`/v1`
  or `/api/v1`) before appending the fixed endpoint. Reuse the existing
  upstream URL validation, proxy/TLS transport, redirect prohibition,
  bounded timeout/body size, and credential-safe diagnostics. Never store
  full upstream responses, headers, or tokens in a snapshot or log.
- Reuse the existing billing probe's periodic runner and bounded execution
  infrastructure, but maintain independent opt-in, due time, failure
  state, and snapshot for information probes. A disabled billing probe or
  failed billing endpoint must not disable or delay an information probe.
  An enabled information probe defaults to a 30-minute refresh interval;
  failure preserves the last value as stale and schedules a bounded retry.
  Concurrent updates use repository compare-and-swap checks on account
  identity, configuration, and previous snapshot so an in-flight probe
  cannot resurrect disabled or old information.

## UI and verification

The new column follows "Upstream Declared Rate" and uses existing table
patterns in light/dark themes, empty state, narrow viewports, and i18n
(Chinese and English). Show current, stale, unsupported, and failed states
without conflating unknown with zero; disclose last successful/attempt
times where useful. Do not alter the existing billing-rate column.

Regression tests cover default/disabled compatibility, platform/feature
validation and editing, exact endpoint paths and URL prefixes, response
normalization (wallet/quota/subscription and image quota), 401/403,
malformed/oversized/timeout/redirect errors, no secret exposure, stale
snapshots, CAS conflicts, independent scheduling when billing is off, the
compact ETag projection, modal persistence, and list rendering. Run
targeted Go and Vue tests, TypeScript checks, and a frontend build before
the PR. No test needs production credentials.
