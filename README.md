# idaas-go-akless-tencent-adapter

[![Go Version](https://img.shields.io/badge/go-1.18%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Development Status](https://img.shields.io/badge/status-Beta-orange)](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter)

English | [简体中文](README_zh.md)

Go SDK for the IDaaS (Identity as a Service) AKless Adapter — obtain STS temporary credentials through IDaaS PAM (Privileged Access Management) to access Tencent Cloud services without SecretKey.

## Features

- **AK-Free Authentication**: No long-term SecretKey required. Uses OIDC Token to obtain STS temporary credentials via IDaaS PAM, reducing the risk of credential leakage
- **Multi-SDK Adaptation**: Provides credential provider for Tencent Cloud SDK, also compatible with COS SDK
- **Auto Credential Refresh**: Built-in credential caching with the core SDK's three-point lifetime model
  (prefetch at 2/3 of the lifetime, synchronous refresh at 4/5, anti-thundering-herd jitter), so credentials
  rotate transparently and an expired credential is never handed downstream
- **Context-Aware**: `GetCredentialsWithContext(ctx)` lets request-scoped cancellation and deadlines bound
  credential acquisition instead of blocking on a refresh
- **Easy Integration**: Factory functions provide one-line credential provider creation, minimizing integration cost

## Requirements

- Go >= 1.18
- Dependencies:
  - idaas-go-core-sdk >= 0.1.0-beta.1

## Installation

```bash
go get github.com/cloud-idaas/idaas-go-akless-tencent-adapter
```

## Prerequisites

This SDK depends on [idaas-go-core-sdk](https://github.com/cloud-idaas/idaas-go-core-sdk). You need to complete the IDaaS Core SDK initialization before using this adapter.

1. Add the `idaas-go-core-sdk` dependency and complete the configuration. See [idaas-go-core-sdk README](https://github.com/cloud-idaas/idaas-go-core-sdk/blob/main/README.md) for details.

2. In the configuration file, set the `scope` to the IDaaS PAM built-in scope:

   ```json
   {
       "scope": "urn:cloud:idaas:pam|cloud_account_role:obtain_access_credential"
   }
   ```

3. Complete the IDaaS Core SDK initialization:

   ```go
   import (
       "github.com/cloud-idaas/idaas-go-core-sdk/factory"
   )

   func main() {
       // factory.Init() loads the config by priority:
       // CLOUD_IDAAS_CONFIG_PATH -> ~/.cloud_idaas/client-config.json
       // GetInstance()/Initialize() are deprecated since core SDK v0.1.0-beta.1.
       if err := factory.Init(); err != nil {
           panic(err)
       }
   }
   ```

## Quick Start

The simplest way to use this SDK is through the factory functions:

```go
package main

import (
	"fmt"
	"log"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
)

func main() {
	// 1. Initialize IDaaS Core SDK
	// factory.Init() loads the config by priority (CLOUD_IDAAS_CONFIG_PATH ->
	// ~/.cloud_idaas/client-config.json). GetInstance()/Initialize() are deprecated
	// since core SDK v0.1.0-beta.1; use the package-level Init/InitWithConfig.
	if err := factory.Init(); err != nil {
		panic(err)
	}

	// 2. Create TencentCloud credentials provider via factory
	provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
	if err != nil {
		log.Fatal(err)
	}
	cred, err := provider.GetCredentials()
	if err != nil {
		log.Fatal(err)
	}

	// 3. Use the TencentCloud temporary credential
	fmt.Println(cred.SecretId)
	fmt.Println(cred.SecretKey)
	fmt.Println(cred.Token)
}
```

## Usage Examples

The following examples are the complete runnable code under [`samples/`](samples).

### Tencent Cloud SDK (tencentcloud-sdk-go)

List CLS log topics, see [samples/list-cls-topics](samples/list-cls-topics):

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// 每步都打进度日志：一旦"看起来没响应"，可以直接从最后一条日志判断卡在配置、PAM 还是腾讯云。
const (
	credentialTimeout = 30 * time.Second
)

// roleArn / region 从环境变量取，让示例无需改代码即可跑真实环境：
//
//	TENCENT_CLOUD_ROLE_ARN —— PAM 中配置的腾讯云账号角色外部标识
//	TENCENT_CLOUD_REGION   —— 地域，默认 ap-guangzhou
func roleArn() string {
	v := os.Getenv("TENCENT_CLOUD_ROLE_ARN")
	if v == "" {
		log.Fatal("缺 TENCENT_CLOUD_ROLE_ARN：请先 export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'（PAM 中配置的腾讯云账号角色外部标识）")
	}
	return v
}

func region() string {
	if v := os.Getenv("TENCENT_CLOUD_REGION"); v != "" {
		return v
	}
	return "ap-guangzhou"
}

// describeConfigSource 说明配置将从哪里加载，排障时第一眼就要看到这个。
func describeConfigSource() string {
	if p := os.Getenv("CLOUD_IDAAS_CONFIG_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return fmt.Sprintf("%s（CLOUD_IDAAS_CONFIG_PATH，文件不可读：%v）", p, err)
		}
		return p + "（CLOUD_IDAAS_CONFIG_PATH）"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloud_idaas/client-config.json") + "（默认路径，未设 CLOUD_IDAAS_CONFIG_PATH）"
}

// newClient 每次都用当前凭证新建一个 CLS 客户端。
//
// 为什么这样写：common.NewTokenCredential 返回的是"取一次就固定"的值对象，腾讯云 SDK
// 不会回调本适配器刷新它；而它的字段在签名时被 SDK 无锁读取，后台 goroutine 直接改字段
// 会与在飞请求构成数据竞争。客户端构造很轻，**到期后重建客户端**是 race-free 的姿势。
// COS 侧不同：cos.AuthorizationTransport.SetCredential 自带读写锁，可原地轮换，
// 见 samples/list-cos-buckets。
func newClient(provider *pam.IDaaSPamTencentCloudCredentialsProvider) (*cls.Client, error) {
	cred, err := provider.GetCredentials()
	if err != nil {
		return nil, err
	}

	httpProfile := profile.NewHttpProfile()
	// 用地域化 endpoint（集成实测可用）；全局 cls.tencentcloudapi.com 在部分地域不可路由。
	httpProfile.Endpoint = fmt.Sprintf("cls.%s.tencentcloudapi.com", region())
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile = httpProfile

	// 临时凭证必须带 Token（STS 三元组缺一不可）。
	return cls.NewClient(common.NewTokenCredential(cred.SecretId, cred.SecretKey, cred.Token), region(), clientProfile)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// 1. Initialize IDaaS Core SDK
	// factory.Init() 按三级优先级加载配置（CLOUD_IDAAS_CONFIG_PATH ->
	// ~/.cloud_idaas/client-config.json）；GetInstance()/Initialize() 在核心 SDK
	// v0.1.0-beta.1 中已 Deprecated，改用包级 Init。
	log.Printf("[1/4] 初始化 IDaaS Core SDK，配置来源：%s", describeConfigSource())
	if err := factory.Init(); err != nil {
		log.Fatalf("[1/4] 初始化失败：%v", err)
	}

	// 2. Create TencentCloud credentials provider via factory
	log.Printf("[2/4] 创建腾讯云凭证提供者，roleArn=%s region=%s", roleArn(), region())
	provider, err := pam.GetTencentCloudCredentialsProvider(roleArn())
	if err != nil {
		log.Fatalf("[2/4] 创建提供者失败：%v", err)
	}
	defer provider.Close()
	log.Printf("[2/4] 提供者就绪，PAM endpoint=%s，超时 connect=%dms read=%dms",
		provider.GetDeveloperApiEndpoint(), provider.GetConnectTimeout(), provider.GetReadTimeout())

	// 3. 换取 STS —— 卡在这一步说明是 IDaaS/PAM 侧网络或配置问题
	ctx, cancel := context.WithTimeout(context.Background(), credentialTimeout)
	defer cancel()
	start := time.Now()
	log.Printf("[3/4] 调用 PAM 换取腾讯云 STS（最长等 %s）...", credentialTimeout)
	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		log.Fatalf("[3/4] 换取凭证失败（耗时 %s）：%v", time.Since(start).Round(time.Millisecond), err)
	}
	log.Printf("[3/4] 凭证到手，耗时 %s；SecretId=%s... 过期时间=%s",
		time.Since(start).Round(time.Millisecond), firstN(cred.SecretId, 6), cred.Expiration)

	// 4. 调用腾讯云接口 —— 卡在这一步说明是腾讯云 API 侧问题
	client, err := newClient(provider)
	if err != nil {
		log.Fatalf("[4/4] 构建 CLS 客户端失败：%v", err)
	}
	log.Printf("[4/4] 调用 CLS DescribeTopics（endpoint=%s）...", fmt.Sprintf("cls.%s.tencentcloudapi.com", region()))
	response, err := client.DescribeTopics(cls.NewDescribeTopicsRequest())
	if err != nil {
		log.Fatalf("[4/4] DescribeTopics 失败：%v", err)
	}
	log.Printf("[4/4] 成功，返回 %d 个 topic", len(response.Response.Topics))
	for _, topic := range response.Response.Topics {
		fmt.Printf("TopicId: %s | TopicName: %s | LogsetId: %s\n",
			*topic.TopicId, *topic.TopicName, *topic.LogsetId)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

### COS (cos-go-sdk-v5)

List COS buckets, see [samples/list-cos-buckets](samples/list-cos-buckets):

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	"github.com/tencentyun/cos-go-sdk-v5"
)

const (
	credentialTimeout = 30 * time.Second
	rotateInterval    = 5 * time.Minute
)

// roleArn / region 的读取姿势与 list-cls-topics 一致：环境变量优先，未设置地域则默认 ap-guangzhou。
func roleArn() string {
	v := os.Getenv("TENCENT_CLOUD_ROLE_ARN")
	if v == "" {
		log.Fatal("缺 TENCENT_CLOUD_ROLE_ARN：请先 export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'（PAM 中配置的腾讯云账号角色外部标识）")
	}
	return v
}

func region() string {
	if v := os.Getenv("TENCENT_CLOUD_REGION"); v != "" {
		return v
	}
	return "ap-guangzhou"
}

func describeConfigSource() string {
	if p := os.Getenv("CLOUD_IDAAS_CONFIG_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return fmt.Sprintf("%s（CLOUD_IDAAS_CONFIG_PATH，文件不可读：%v）", p, err)
		}
		return p + "（CLOUD_IDAAS_CONFIG_PATH）"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloud_idaas/client-config.json") + "（默认路径，未设 CLOUD_IDAAS_CONFIG_PATH）"
}

// refreshTransport 把适配器当前凭证写入 COS 传输层。
//
// cos.AuthorizationTransport 用读写锁保护凭证字段（SetCredential/GetCredential），
// 因此可以在后台 goroutine 里原地轮换，无需重建 client。适配器自带缓存，
// GetCredentials 命中缓存时不发网络请求，周期调用很轻。
func refreshTransport(ctx context.Context, provider *pam.IDaaSPamTencentCloudCredentialsProvider, transport *cos.AuthorizationTransport) error {
	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		return err
	}
	transport.SetCredential(cred.SecretId, cred.SecretKey, cred.Token)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// 1. Initialize IDaaS Core SDK
	// factory.Init() 按三级优先级加载配置（CLOUD_IDAAS_CONFIG_PATH ->
	// ~/.cloud_idaas/client-config.json）；GetInstance()/Initialize() 在核心 SDK
	// v0.1.0-beta.1 中已 Deprecated，改用包级 Init。
	log.Printf("[1/4] 初始化 IDaaS Core SDK，配置来源：%s", describeConfigSource())
	if err := factory.Init(); err != nil {
		log.Fatalf("[1/4] 初始化失败：%v", err)
	}

	// 2. Create TencentCloud credentials provider via factory
	log.Printf("[2/4] 创建腾讯云凭证提供者，roleArn=%s region=%s", roleArn(), region())
	provider, err := pam.GetTencentCloudCredentialsProvider(roleArn())
	if err != nil {
		log.Fatalf("[2/4] 创建提供者失败：%v", err)
	}
	defer provider.Close()
	log.Printf("[2/4] 提供者就绪，PAM endpoint=%s，超时 connect=%dms read=%dms",
		provider.GetDeveloperApiEndpoint(), provider.GetConnectTimeout(), provider.GetReadTimeout())

	// 3. 换取 STS —— 卡在这一步说明是 IDaaS/PAM 侧网络或配置问题
	ctx, cancel := context.WithTimeout(context.Background(), credentialTimeout)
	defer cancel()
	start := time.Now()
	log.Printf("[3/4] 调用 PAM 换取腾讯云 STS（最长等 %s）...", credentialTimeout)
	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		log.Fatalf("[3/4] 换取凭证失败（耗时 %s）：%v", time.Since(start).Round(time.Millisecond), err)
	}
	log.Printf("[3/4] 凭证到手，耗时 %s；SecretId=%s... 过期时间=%s",
		time.Since(start).Round(time.Millisecond), firstN(cred.SecretId, 6), cred.Expiration)

	// 4. Use TencentCloud credential to create TencentCloud client
	u, _ := url.Parse(fmt.Sprintf("https://cos.%s.myqcloud.com", region()))
	b := &cos.BaseURL{BucketURL: u}
	transport := &cos.AuthorizationTransport{}
	client := cos.NewClient(b, &http.Client{Transport: transport})

	transport.SetCredential(cred.SecretId, cred.SecretKey, cred.Token)

	// 长跑进程：后台按固定周期把适配器凭证同步进 transport（适配器会在 2/3 寿命点预取、
	// 4/5 寿命点同步刷新，这里只需保证 transport 跟得上）。一次性 CLI 可删掉这段。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(rotateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := refreshTransport(context.Background(), provider, transport); err != nil {
					log.Printf("后台轮换 COS 凭证失败（不致命，下次会重试）：%v", err)
				}
			case <-stop:
				return
			}
		}
	}()

	log.Printf("[4/4] 调用 COS Service.Get 列举 bucket（endpoint=%s）...", u)
	result, _, err := client.Service.Get(context.Background())
	if err != nil {
		log.Fatalf("[4/4] 列举 bucket 失败：%v", err)
	}
	log.Printf("[4/4] 成功，返回 %d 个 bucket", len(result.Buckets))
	for _, bucket := range result.Buckets {
		fmt.Printf("Bucket: %s | Region: %s | CreateDate: %s\n",
			bucket.Name, bucket.Region, bucket.CreationDate)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

### Running The Samples

Samples take the role identifier and region from the environment, so nothing needs to be edited
to run them against a real tenant:

```bash
export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json
# 交互式输入（无回显、有提示；zsh 语法，不要把整块命令一起粘贴）
read -rs "IDAAS_CLIENT_SECRET?client secret: " && export IDAAS_CLIENT_SECRET
export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'
export TENCENT_CLOUD_REGION=ap-guangzhou              # optional

go run ./samples/list-cls-topics     # CLS DescribeTopics
go run ./samples/list-cos-buckets    # COS Service.Get (list buckets) + background rotation
```

Both samples log a `[1/4]…[4/4]` progress line per stage, so a run that looks "stuck" names
the stage it is stuck at: `[1/4]` config load, `[2/4]` provider construction, `[3/4]` PAM
credential exchange (capped at 30s by the sample's context), `[4/4]` the Tencent Cloud API call.
The secret prompt uses `read -rs "<VAR>?prompt: "` (zsh) — a bare `read -s VAR` has **no prompt**,
looks like a hang, and swallows the next pasted line as its input.

Missing `TENCENT_CLOUD_ROLE_ARN` exits before calling PAM, with the `export` line to copy.
The CLS sample uses the regional endpoint `cls.<region>.tencentcloudapi.com`.

### Custom Parameters

You can use the options pattern for fine-grained control:

```go
provider, err := pam.NewIDaaSPamTencentCloudCredentialsProvider(
    pam.WithDeveloperApiEndpoint("https://developer-api.example.com"),
    pam.WithIdaasInstanceId("your-idaas-instance-id"),
    pam.WithOidcTokenProvider(yourOidcTokenProvider),
    pam.WithRoleArn("your-role-arn"),
    pam.WithConnectTimeout(5000), // 0 = default; legal range [2000, 60000] ms
    pam.WithReadTimeout(10000),   // 0 = default; legal range [2000, 60000] ms
    // Optional extras:
    pam.WithStaleValueBehavior(enums.StaleValueBehaviorBlock),
    pam.WithHttpClient(sdkhttp.NewDefaultHttpClient(5000, 10000).WithTracePropagator(myPropagator)),
)
```

| Option | Default | Notes |
|--------|---------|-------|
| `WithDeveloperApiEndpoint` | — (required) | Must include the scheme; obtained automatically from the core SDK factory in the convenience path |
| `WithIdaasInstanceId` | — (required) | Escaped into the URL path with `url.PathEscape` |
| `WithOidcTokenProvider` | — (required) | `provider.OidcTokenProvider`; bridge a core SDK credential provider, or inject a stub in tests |
| `WithRoleArn` | — (required) | Sent as the `cloudAccountRoleExternalId` query parameter |
| `WithConnectTimeout` / `WithReadTimeout` | 5000 / 10000 ms | `0` keeps the default; out-of-range values are rejected at construction (`ConnectTimeoutNotValid` / `ReadTimeoutNotValid`), matching the core SDK config validator |
| `WithStaleValueBehavior` | `REFRESH` | `BLOCK` / `ALLOW` / `REFRESH`; empty normalizes to `REFRESH`, anything else is rejected (`InvalidStaleValueBehavior`) |
| `WithHttpClient` | core SDK default client | Inject your own client for proxies, mTLS, custom TLS or trace propagation. Once injected, the adapter no longer builds a client: timeouts, TLS and trace are entirely the host client's responsibility |

### Configuration Scope

Not every core SDK config key affects this adapter — the table below states exactly what does:

| Core SDK config | Affects adapter PAM calls? | How to control it |
|-----------------|----------------------------|-------------------|
| `httpConfiguration.connectTimeout` / `readTimeout` | **Yes** via `GetTencentCloudCredentialsProvider` | Or override per-provider with `WithConnectTimeout` / `WithReadTimeout` |
| `httpConfiguration.unsafeIgnoreSslCert` | **Yes** via `GetTencentCloudCredentialsProvider` only | The adapter exposes **no** option to disable certificate verification. If you really need it, inject your own client through `WithHttpClient` and own that decision |
| `staleValueBehavior` | **No** — core SDK beta.1 has no package-level getter for it | Use `WithStaleValueBehavior`. Note the adapter refuses to return an expired credential even under `ALLOW` (see below) |
| `factory.WithTracePropagator` | **No** (no getter either) | Inject a client built with `sdkhttp.NewDefaultHttpClient(...).WithTracePropagator(p)` via `WithHttpClient` |

Behavioural invariants that hold regardless of the settings above:

- **An expired credential is never handed out.** If a refresh fails while the cached credential is genuinely expired, the call fails fast with `CredentialExpired` instead of returning stale material that would surface downstream as an opaque 403.
- **Cold-start bursts fan out.** The core SDK reads its cache outside the lock and registers the refresh leader inside it, so the two steps are not atomic: with 16 concurrent first-fetches the adapter measured up to 7 PAM calls (theoretical upper bound = concurrency). It is a one-time amplification at cold start, and every caller still receives the same valid credential. Steady-state concurrent reads issue **zero** PAM calls (covered by tests), so never build a rate-limit or assertion on "exactly 1 call per N concurrent requests".

## API Reference

### Factory Functions

| Function | Return Type | Description |
|----------|-------------|-------------|
| `GetTencentCloudCredentialsProvider(roleArn)` | `*IDaaSPamTencentCloudCredentialsProvider` | Create a Tencent Cloud credentials provider |

### Credential Provider Methods

| Method | Return Type | Description |
|--------|-------------|-------------|
| `GetCredentials()` | `(*TencentCloudCredential, error)` | Return the cached credential, refreshing it when it enters the stale window. The returned value is a **copy** — mutating it cannot pollute the cache |
| `GetCredentialsWithContext(ctx)` | `(*TencentCloudCredential, error)` | Same as above, but the caller's `context` bounds the wait: cancellation/deadline returns immediately instead of blocking on an in-flight refresh |
| `Close()` | `error` | Release provider-held resources (idle connections, prefetch strategy). Idempotent; the provider stays usable afterwards |
| `GetRoleArn()` / `GetIdaasInstanceId()` / `GetDeveloperApiEndpoint()` / `GetOIDCToken()` / `GetConnectTimeout()` / `GetReadTimeout()` | various | Read-only accessors, mainly for diagnostics |

> Since core SDK `v0.1.0-beta.1`, refresh failures inside the degradation buffer still return the
> cached credential, but once it is genuinely expired the provider fails fast with error code
> `CredentialExpired` instead of handing an expired credential to the Tencent Cloud SDK.

### TencentCloudCredential

| Field | Type | Description |
|-------|------|-------------|
| `SecretId` | `string` | Temporary SecretId |
| `SecretKey` | `string` | Temporary SecretKey |
| `Token` | `string` | Session Token |
| `Expiration` | `time.Time` | Credential expiration time |

### Error Codes

| Code | Meaning | Retry guidance |
|------|---------|----------------|
| `InvalidParameter` | A required option is missing/blank | No — fix the configuration |
| `ConnectTimeoutNotValid` / `ReadTimeoutNotValid` | Timeout outside `[2000, 60000]` ms | No — fix the value |
| `InvalidStaleValueBehavior` | `staleValueBehavior` outside `BLOCK` / `ALLOW` / `REFRESH` | No — fix the value |
| `OidcTokenError` | Could not obtain the OIDC token from the core SDK | Yes — check core SDK credentials/config and endpoint reachability |
| `HttpError` | Transport-level failure calling PAM | Yes — back off; the adapter also degrades to a cached credential when one is still valid |
| `PamApiError` | PAM returned a non-2xx response (status and truncated body included) | Depends on status |
| `ParseError` | PAM response missing/malformed fields | No — usually a contract or server-side issue |
| `CredentialExpired` | Cached credential is genuinely expired and refresh is unavailable | Yes after PAM recovers — the adapter refuses to hand out expired credentials |

`ConnectTimeoutNotValid`, `ReadTimeoutNotValid` and `InvalidStaleValueBehavior` are the core SDK's own codes (`idaas-go-core-sdk/constants`), reused here so upstream error-code routing stays uniform.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `CLOUD_IDAAS_CONFIG_PATH` | IDaaS configuration file path |

## Upgrading from v0.1.0-alpha.3

Core SDK `v0.1.0-beta.1` is a breaking release; this adapter has been migrated to it.

**Compilation-breaking (you must change code):**
- `provider.OidcTokenProvider` gained `GetOidcTokenWithContext(ctx)`. Custom OIDC providers must implement it — the adapter's convenience path already bridges the core SDK provider for you.
- `cache.NewCachedResultSupplier` dropped its third `prefetchThreshold` argument, and `cache.RefreshResult` gained `StaleTime` / `PrefetchTime` while losing its `Error` field (errors now come from the refresh function's `error` return).
- `factory.GetInstance()` / `Initialize()` / `CreateCredentialProvider()` are deprecated (scheduled for removal); use the package-level `factory.Init` / `InitWithConfig` / `GetIDaaSCredentialProvider`.

**Behaviour changes to know about:**
- `GetCredentials()` returns a copy of the cached credential; the old implementation returned the cached pointer.
- Expiry-related failures now surface as `CredentialExpired` instead of `ParseError` or a raw cache error, and refresh failures no longer leak the raw 2xx response body into error messages.
- Printing a `TencentCloudCredential` (`%v` / `%+v` / `%#v`) is redacted: `SecretKey` / `Token` no longer land in logs in plaintext. Read the fields explicitly when you need the values.
- Timeout options are validated against `[2000, 60000] ms`; previously out-of-range values were silently accepted.

## Integration Tests

Real-environment tests live behind the `integration` build tag and are **skipped by default**
(`go test ./...` never touches them). They hit the IDaaS token endpoint, the PAM Developer API
and, optionally, a live Tencent Cloud API.

```bash
# 1) Config: copy samples/cloud_idaas.json to <repo root>/cloud_idaas.json (git-ignored) and
#    fill in your own instance; or point CLOUD_IDAAS_CONFIG_PATH at any other path.
export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json
# 2) Client secret: the variable name comes from authnConfiguration.clientSecretEnvVarName.
export IDAAS_CLIENT_SECRET='<client secret>'
# 3) External identifier of the Tencent Cloud account role configured in PAM.
export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'

# Wiring check only (no network): endpoint normalization, httpConfiguration propagation,
# instanceId / roleArn plumbing.
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration_ConfigWiring -v

# Full chain (exchange STS + hit a live CLS API).
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration -v -timeout 5m
```

| Switch | Effect |
|--------|--------|
| `IDAAS_INTEGRATION=on` | Master switch; anything else skips every case |
| `TENCENT_API_PROBE=off` | Skip the live CLS `DescribeTopics` probe (credential path still runs) |
| `TENCENT_CLOUD_REGION` | Region for the probe, default `ap-guangzhou` |
| `IDAAS_INTEGRATION_TIMEOUT` | Per-call timeout for credential acquisition, default `30s` |

Missing inputs fail with an actionable message rather than an opaque network error. The probe
counts "authorized but denied" (AccessDenied/UnauthorizedOperation) as **pass** — it proves the
credential and signature were accepted — while `InvalidSecretId` / `InvalidToken` /
`FailedToAuthenticate` are treated as failures. No credential is ever printed in plaintext
(`redact()` summaries only).

`cloud_idaas.json` and other local config/secret files are gitignored on purpose (repo root only);
`samples/cloud_idaas.json` is a committable placeholder template to copy from.

## Support & Feedback

- **Issues & Feedback**: For questions or suggestions, please submit an [Issue](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter/issues)
- **Security Disclosures**: See [Security Policy](SECURITY.md) for the responsible-reporting process.

## License

This project is licensed under the [Apache License 2.0](LICENSE).
