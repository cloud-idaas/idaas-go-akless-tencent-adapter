# Security Policy

This adapter obtains **Tencent Cloud STS temporary credentials**
(`tmpSecretId` / `tmpSecretKey` / `token`) from the IDaaS PAM Developer API using an OIDC
token minted by the IDaaS Core SDK, and adapts them to
`github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common.CredentialsProvider` and
`cos-go-sdk-v5`. Security is a top priority. This document describes how to report
vulnerabilities responsibly and what to expect.

## Supported versions

Only the latest minor release line receives security fixes. The project is currently in
**0.1.x beta**; breaking changes may occur before 1.0 GA.

| Version | Supported           |
| ------- | ------------------- |
| 0.1.x   | ✅ latest beta line  |
| < 0.1.0 | ❌ not supported     |

## Reporting a vulnerability

**Do not open a public GitHub issue** for a suspected security vulnerability.

Report it privately via
[GitHub Security Advisory → "Report a vulnerability"](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter/security/advisories/new).

Please include:

- A description of the issue and its potential impact.
- The version / commit you tested against (`go list -m github.com/cloud-idaas/idaas-go-akless-tencent-adapter`).
- A minimal reproduction. **Never include real credentials** — no `tmpSecretId` /
  `tmpSecretKey` / `token`, no OIDC token, no `IDAAS_CLIENT_SECRET`, no real
  `idaasInstanceId` / `clientId`. Use synthetic values or redact them.
- Whether the same issue affects the sibling adapters (`idaas-go-akless-alibabacloud-adapter`,
  `idaas-go-akless-aws-adapter`) or `idaas-go-core-sdk`, so we can coordinate one fix.

## Response timeline

We acknowledge receipt within **3 business days** and aim to give an initial assessment within
**7 business days**. A fix or mitigation ships as soon as practicable, with public disclosure
coordinated after the fix is available.

## Scope

In scope:

- Credential leakage (`tmpSecretId` / `tmpSecretKey` / `token` / OIDC token / client secret)
  through error messages, logs, `fmt` output, or the credential `String()` representation.
  `domain.TencentCloudCredential` is required to stay redacted under `%v` / `%+v` / `%#v`;
  a regression here is a security bug, not a cosmetic one.
- Authentication or authorization bypass anywhere in the OIDC → PAM → STS refresh chain.
- Cache / staleness defects that can serve an **expired** credential to a downstream Tencent
  Cloud SDK, or that keep using a credential after PAM revokes it. The 2xx-but-already-expired
  response path (`CredentialExpired`) is part of this contract.
- URL / header injection in the PAM request construction, and path injection via the
  `idaasInstanceId` interpolated into the request path (it is `url.PathEscape`d — a bypass is
  in scope).
- TLS verification bypass reachable from a normal (non-debug) configuration.
- Any code path that puts a raw PAM response body into an error message.

Out of scope:

- Vulnerabilities in upstream dependencies — report them upstream
  (`idaas-go-core-sdk`, `tencentcloud-sdk-go`, `cos-go-sdk-v5`). You may still open an issue
  here so we can pin a fixed version.
- Findings obtained by running this adapter against an IDaaS instance or Tencent Cloud account
  you do not own or are not authorized to test.
- Physical access, social engineering, or attacks against Tencent Cloud / IDaaS service
  availability (DoS).

## Known risk notes

- **Local-development TLS escape hatch.** The Core SDK's `httpConfiguration.unsafeIgnoreSslCert`
  disables certificate verification. Keep it `false` in production; the adapter refuses to
  inherit it silently and only uses it when the tenant configuration explicitly sets it.
- **Timeout bounds.** `connectTimeout` / `readTimeout` are validated against `[2000, 60000] ms`.
  Values outside the range are rejected rather than silently accepted, so a PAM outage cannot
  turn into an unbounded stall in the caller's thread.
- **No credential persistence.** STS credentials live only in the in-process cache; nothing is
  written to disk. Do not add on-disk caching without a security review.
- **Integration tests are opt-in and never run in CI.** They require a real instance ID, a real
  client secret in `IDAAS_CLIENT_SECRET`, and a real role external identifier
  (`TENCENT_CLOUD_ROLE_ARN`); CI has none of these, and the local config files are git-ignored.

## Safe disclosure

We are committed to acknowledging reporters. If you want to be credited in the release notes,
include a name or handle in your report — we will credit you unless you ask to stay anonymous.
Good-faith reporting is appreciated and will not result in legal action.
