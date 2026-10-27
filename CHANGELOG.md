# Changelog

All notable changes to the IDaaS Tencent Cloud akless adapter are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0-beta.1] - 2026-10-27

Migrated the adapter to `idaas-go-core-sdk v0.1.0-beta.1` (a breaking core release) and
closed the review findings on credential-cache correctness, context lifecycle and
tenant-configuration propagation.

### Added

- `GetCredentialsWithContext(ctx)` — the caller's context now bounds the *wait* for a
  credential. Cancellation/deadline returns immediately (`ClientRequestCanceled`) instead of
  blocking on an in-flight refresh; the refresh itself continues and commits to the cache,
  so later requests still hit it. `GetCredentials()` delegates to it with
  `context.Background()`, and a `nil` ctx is tolerated (treated as `Background()`) because
  upstream Tencent Cloud SDKs may pass one.
- `Close()` — releases idle connections via `http.ResourceCloser` and closes the core SDK
  result supplier. Idempotent; the provider remains usable.
- `pam/integration_test.go` — real-environment suite behind the `integration` build tag
  (this repo had none), gated by `IDAAS_INTEGRATION=on` and skipped by `go test ./...`:
  `TestIntegration_ConfigWiring` (no network — asserts the bare-host endpoint is normalized to
  `https://` and that `httpConfiguration` timeouts reach the adapter),
  `TestIntegration_ObtainTencentCredential` (OIDC token → PAM → STS, field completeness,
  cache hit on the second call), `TestIntegration_CallerContextBoundsWait`, and
  `TestIntegration_TencentCloudApiProbe` (live CLS `DescribeTopics`; "authorized but denied"
  counts as pass, credential-shaped errors fail). Missing inputs fail with the exact command to
  fix them; credentials are only logged as `redact()` summaries.
- `.gitignore` now excludes local real configs (`cloud_idaas.json`, `client-config.json`,
  `*.env`) so tenant configuration cannot be committed.
- Options `WithStaleValueBehavior` (`BLOCK` / `ALLOW` / `REFRESH`, empty → `REFRESH`) and
  `WithHttpClient` (inject a custom client for proxies, mTLS, custom TLS or trace
  propagation). `withHttpClient` was test-only and unexported; it is now the public
  injection point.
- `constants/error_code.go` — the adapter's error codes are now named constants aligned with
  `idaas-go-akless-alibabacloud-adapter` (`InvalidParameter`, `OidcTokenError`, `HttpError`,
  `PamApiError`, `ParseError`, `CredentialExpired`) instead of inline literals.
- `TencentCloudCredential.String()/GoString()` — redact `SecretId` / `SecretKey` / `Token`
  (same scheme as the core SDK's `logger.Redact`) so a credential cannot be leaked by an
  accidental `%v` in logs. Value receivers are deliberate: a pointer receiver would let
  `%+v` on a dereferenced value print everything.
- `compile-time interface assertions` for `provider.OidcTokenProvider`.
- Test suite for `pam` and `domain` (none existed): success path, request construction,
  caching, 12 error branches, context cancellation, three-lifetime model against an injected
  clock, expiry fail-fast, caller-mutation isolation, concurrent access under `-race`,
  option validation, secret redaction.
- `CHANGELOG.md` (this file).

### Changed

- **Breaking** `provider.OidcTokenProvider` now requires `GetOidcTokenWithContext(ctx)`
  (Go has no default methods); callers with their own OIDC provider implementation must add
  the method.
- **Breaking** `cache.RefreshResult` no longer has an `Error` field, and
  `cache.NewCachedResultSupplier` no longer takes a `prefetchThreshold` argument; the three
  lifetimes (`PrefetchTime` 2/3, `StaleTime` 4/5, `ExpiresAt`) are built by
  `cache.NewRefreshResult`, which also applies anti-thundering-herd jitter. Previously the
  adapter mis-used `ExpiresAt` to carry the prefetch instant.
- **Breaking** `factory.GetInstance()` / `Initialize()` / `CreateCredentialProvider()` are
  deprecated in the core SDK; the convenience entry uses the package-level
  `GetIDaaSCredentialProvider` / `GetDeveloperApiEndpoint` / `GetIDaaSInstanceId`, and
  samples/README use `factory.Init()`.
- **Breaking** `GetCredentials()` returns a copy of the cached credential instead of the
  cached pointer.
- **Breaking** timeouts passed through `WithConnectTimeout` / `WithReadTimeout` are validated
  against `[2000, 60000] ms` (0 = keep default), matching the core SDK config validator;
  out-of-range values used to be silently accepted.
- Default timeouts now reference the core SDK's single-source constants
  (`constants.DefaultConnectTimeoutMs` / `DefaultReadTimeoutMs`) instead of duplicating the
  literal values.
- Expiry conditions report `CredentialExpired` rather than `ParseError` / a raw cache error,
  and an expired credential is never returned even under `ALLOW`.
- Error messages no longer embed the raw PAM response body when credential fields are missing
  or empty — a 2xx body contains issued credentials, which must not travel with an error
  object into upstream logs.
- `idaasInstanceId` is escaped into the URL path with `url.PathEscape`.
- Samples read `TENCENT_CLOUD_ROLE_ARN` / `TENCENT_CLOUD_REGION` from the environment instead
  of hard-coding `"your-role-arn"`, and exit with a copy-pastable `export` hint *before*
  calling PAM when it is missing. The CLS sample now uses the regional endpoint
  `cls.<region>.tencentcloudapi.com` (verified against a live IDaaS + Tencent Cloud setup; the
  global endpoint is not routable for every region).
- Samples print `[1/4]…[4/4]` progress logs (config source, provider construction, PAM
  exchange with a 30s context deadline, the cloud API call) so a run that looks silent is
  diagnosable, and cap credential acquisition with `context.WithTimeout` instead of waiting on
  socket timeouts only.
- Samples demonstrate credential rotation instead of a one-shot static credential: the COS
  sample refreshes `cos.AuthorizationTransport` in place (`SetCredential` is lock-protected);
  the CLS sample rebuilds the client, since `common.Credential` fields are read without
  locking by the Tencent Cloud SDK and mutating them concurrently would race.
- README/README_zh: dependency bumped to `>= 0.1.0-beta.1`, new "Configuration Scope",
  "Error Codes" and "Upgrading" sections, provider-method table, examples kept in sync with
  `samples/`.

### Fixed

- `GetTencentCloudCredentialsProvider` dereferenced a nil `*IDaaSClientConfig` when the core
  SDK factory had not been initialized (`GetInstance().GetConfig()` returns nil) — now it
  returns `IDaaSCredentialProviderFactoryNotInit`.
- `httpConfiguration` (timeouts, `unsafeIgnoreSslCert`) had no effect on adapter PAM calls
  because the adapter built its own HTTP client: internal endpoints with self-signed
  certificates failed with x509 errors on the PAM call while the core SDK's own token call
  succeeded. The convenience entry now propagates the effective HTTP configuration.
- Refresh failures were invisible: the stale-value fallback discarded the error, so a PAM
  outage surfaced only as an opaque downstream 403. Failures now surface as a typed error and
  the core SDK logs the fallback.
- Error paths that could not recover (already-expired credential, unparsable expiration) no
  longer silently produce a credential with a bogus lifetime.
- `oidcToken` was a plain string written by background prefetch and read by `GetOIDCToken()`
  (a data race under `-race`); it is now an `atomic.Value`.
- `parseUTCDate` accepts fractional seconds (`RFC3339Nano`) and space-separated timestamps;
  without them, a PAM response with milliseconds failed every refresh and drained the
  degradation buffer into blanket `CredentialExpired`.
- `truncateBody` cuts on a UTF-8 rune boundary, so a truncated error body never contains an
  invalid code point that a log pipeline might drop outright.
- The concurrency test asserted "exactly one PAM call per burst", which the core SDK cannot
  guarantee (its cache read is outside the lock that registers the refresh leader) — flaky in
  ~20% of single runs and up to 7 calls under `-count=30` in one process. Split in two:
  `TestGetCredentials_ConcurrentAccessIsSafe` now pins the real steady-state invariant
  (warm cache ⇒ **zero** extra PAM calls under 16 concurrent readers, plus caller-mutation
  isolation and value consistency), and `TestGetCredentials_ColdStartBurstAllCallersSucceed`
  asserts only that every cold-start caller gets a valid credential, with no call-count
  assertion. `go test -count=100` and `-race -count=30` are green.

### Known limitations (upstream gaps, recorded here so they are not mistaken for adapter bugs)

- `staleValueBehavior` in the core SDK config does not reach the adapter's credential cache:
  beta.1 exposes no package-level getter for it (only deprecated `GetInstance().GetConfig()`).
  Use `WithStaleValueBehavior`.
- `factory.WithTracePropagator` does not cover the adapter's outbound PAM call for the same
  reason. Inject a client built with
  `sdkhttp.NewDefaultHttpClient(...).WithTracePropagator(p)` via `WithHttpClient`.
- Both gaps come from the core SDK exposing no package-level getter for these two settings;
  worth requesting one upstream.
