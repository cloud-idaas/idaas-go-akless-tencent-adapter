# Contributing to idaas-go-akless-tencent-adapter

Thank you for taking the time to contribute! 🎉

This project is the **Go** adapter in the IDaaS AKless family: it exchanges an IDaaS OIDC
token for Tencent Cloud STS temporary credentials through the PAM Developer API, so
applications can call CLS / COS / other Tencent Cloud services without long-lived secret ids.
Siblings: `idaas-go-akless-alibabacloud-adapter`, `idaas-go-akless-aws-adapter`, and the
Java / Python adapters.

The guidelines below keep the adapter consistent, secure, and cross-language-aligned.

## Prerequisites

- **Go >= 1.18** — the `go` directive in `go.mod` is the enforced floor. CI compiles the
  dependency graph on a modern toolchain; raising the floor is a deliberate decision (see
  *Coding standards*).
- [`idaas-go-core-sdk`](https://github.com/cloud-idaas/idaas-go-core-sdk) resolved via `go.mod`
  (`go mod tidy` fetches it from the module proxy).
- Familiarity with the Core SDK's `config` / `factory` / `provider` / `cache` / `errors` /
  `http` / `logger` packages — this adapter is a thin layer over them.

## Development workflow

```bash
# 1. From the repo root
go mod tidy

# 2. Format + vet (vet is also run with the integration tag so that suite keeps compiling)
gofmt -w .
go vet ./...
go vet -tags=integration ./...

# 3. Unit tests. CI gates coverage at >= 80% over the library packages
#    (samples/ are standalone main programs and are excluded from the gate).
go test ./domain/... ./pam/... ./constants/... -race -covermode=atomic \
  -coverprofile=coverage.out -coverpkg=./domain/...,./pam/...,./constants/...
go tool cover -func=coverage.out | tail -1

# 4. Everything must still build, samples included
go build ./...

# 5. Vulnerability scan (CI runs this in a separate job on the latest toolchain)
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

### Integration tests (opt-in)

`pam/integration_test.go` sits behind the `integration` build tag and is skipped by
`go test ./...`. It talks to a **real** IDaaS instance, the real PAM Developer API and
(optionally) a real Tencent Cloud API, so it never runs in CI.

```bash
# Config: copy samples/cloud_idaas.json to <repo root>/cloud_idaas.json and fill in your own
# instance, or export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json
export IDAAS_CLIENT_SECRET='<client secret>'                    # name comes from clientSecretEnvVarName
export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'   # role external id configured in PAM

# Wiring check (no network)
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration_ConfigWiring -v

# Full chain (OIDC token -> PAM -> STS -> live CLS probe)
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration -v -timeout 5m

# Skip the live Tencent Cloud probe but still exercise the credential path
IDAAS_INTEGRATION=on TENCENT_API_PROBE=off go test -tags=integration ./pam/ -run TestIntegration -v
```

**Never commit real credentials or a real tenant configuration.** The repo-root
`cloud_idaas.json`, `*.local.json`, `client-config.json` and `.env*` are git-ignored;
test logs only ever print `redact()` summaries (4-char prefix + length + SHA-256 prefix).

## Coding standards

- **Go floor.** Do not raise the `go` directive above 1.18 without a deliberate decision and a
  CHANGELOG entry. In particular avoid `log/slog` (Go 1.21), the builtins `min`/`max`
  (Go 1.21), `errors.Join` (Go 1.20) and `atomic.Pointer[T]` (Go 1.19); use local helpers and the
  Core SDK abstractions instead.
- **Cross-language wire contract.** The PAM request path, the `cloudAccountRoleExternalId` query
  parameter, the response field keys (`cloudAccountRoleAccessCredential` → `tencentCloudStsToken`
  → `tmpSecretId` / `tmpSecretKey` / `token` / `expiration`) and the error-code values are shared
  with the other adapters. They are pinned in [`constants/constants_test.go`](constants/constants_test.go).
  Changing any value is a breaking protocol change: coordinate across languages and confirm it
  against the PAM service.
- **Credential lifetime.** Cache behaviour follows the Core SDK's dual time-point model
  (background prefetch, synchronous refresh near expiry, bounded stale degradation). Any change
  to expiry / `staleIfError` handling must keep "never hand an expired credential to a downstream
  SDK without saying so" true — that path fails with `CredentialExpired`, not with a 403.
- **Context.** Every blocking call takes the caller's `context.Context`; cancellation must return
  promptly (`ClientRequestCanceled`) while the in-flight refresh still completes for later callers.
- **No secrets in errors or logs.** Error messages name *missing fields*; they never embed the raw
  PAM response body (bodies are truncated) or credential values. `fmt`-printing a credential stays
  redacted — covered by a test.
- **Functional options.** New configuration is added as a `With*` option on
  `NewIDaaSPamTencentCloudCredentialsProvider` and surfaced through the convenience entry point
  `GetTencentCloudCredentialsProvider(roleArn)`, mirroring the Java `Builder` / Python
  keyword-argument style. Keep the factory's process-wide `Init` / `Reset` semantics intact.
- **Compile-time interface assertions.** Keep `var _ common.CredentialsProvider = (*T)(nil)` (and
  the COS adapter assertion) so an upstream signature change fails at build time.
- **Comments.** Keep the existing bilingual style: Chinese for behaviour rationale, English for
  exported API docs.

## Pull requests

1. Branch from `main` (`feature/<topic>` or `fix/<topic>`).
2. Keep changes focused — one concern per PR.
3. Record the change in `CHANGELOG.md` under `## [Unreleased]` (Keep a Changelog format).
4. Make CI green: fmt, vet (incl. `-tags=integration`), build, unit tests with ≥ 80 % coverage
   over the library packages, and the vulnerability scan.
5. If you touch the wire contract, the refresh time-point model, or the option surface, update
   `README.md`, `README_zh.md`, `CHANGELOG.md` and the in-code doc comments **together** — they
   have drifted apart before.
6. Add a regression test for every bug fix; explain in the PR which failure mode it prevents.

## Security disclosures

**Do not open a public issue for security vulnerabilities.** See [SECURITY.md](SECURITY.md).

## Code of conduct

Participation in this project is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
Please be kind.
