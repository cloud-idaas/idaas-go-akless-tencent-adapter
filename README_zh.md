# idaas-go-akless-tencent-adapter

[![Go Version](https://img.shields.io/badge/go-1.18%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Development Status](https://img.shields.io/badge/status-Beta-orange)](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter)

[English](README.md) | 简体中文

IDaaS（身份即服务）AKless 适配器 Go SDK —— 通过 IDaaS PAM（特权访问管理）获取 STS 临时凭证，无需 SecretKey 即可访问腾讯云服务。

## 特性

- **免 AK 认证**：无需长期 SecretKey。通过 OIDC Token 经 IDaaS PAM 获取 STS 临时凭证，降低凭证泄露风险
- **多 SDK 适配**：提供腾讯云 SDK 凭证提供者，同时兼容 COS SDK
- **凭证自动刷新**：内置凭证缓存，采用核心 SDK 的三寿命点模型（2/3 寿命预取、4/5 寿命同步刷新、防惊群 jitter），
  凭证无缝轮转且绝不把过期凭证交给下游
- **Context 感知**：`GetCredentialsWithContext(ctx)` 让请求级取消/超时约束凭证获取，不再干等刷新
- **易于集成**：工厂函数提供一行代码创建凭证提供者，最大程度降低集成成本

## 环境要求

- Go >= 1.18
- 依赖：
  - idaas-go-core-sdk >= 0.1.0-beta.1

## 安装

```bash
go get github.com/cloud-idaas/idaas-go-akless-tencent-adapter
```

## 前置条件

本 SDK 依赖 [idaas-go-core-sdk](https://github.com/cloud-idaas/idaas-go-core-sdk)。使用本适配器前，需要完成 IDaaS Core SDK 的初始化。

1. 添加 `idaas-go-core-sdk` 依赖并完成配置。详见 [idaas-go-core-sdk README](https://github.com/cloud-idaas/idaas-go-core-sdk/blob/main/README.md)。

2. 在配置文件中，将 `scope` 设置为 IDaaS PAM 内置 scope：

   ```json
   {
       "scope": "urn:cloud:idaas:pam|cloud_account_role:obtain_access_credential"
   }
   ```

3. 完成 IDaaS Core SDK 初始化：

   ```go
   import (
       "github.com/cloud-idaas/idaas-go-core-sdk/factory"
   )

   func main() {
       // factory.Init() 按优先级加载配置：
       // CLOUD_IDAAS_CONFIG_PATH -> ~/.cloud_idaas/client-config.json
       // GetInstance()/Initialize() 自核心 SDK v0.1.0-beta.1 起已废弃。
       if err := factory.Init(); err != nil {
           panic(err)
       }
   }
   ```

## 快速开始

最简单的使用方式是通过工厂函数：

```go
package main

import (
	"fmt"
	"log"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
)

func main() {
	// 1. 初始化 IDaaS Core SDK
	if err := factory.Init(); err != nil {
		panic(err)
	}

	// 2. 通过工厂创建腾讯云凭证提供者
	provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
	if err != nil {
		log.Fatal(err)
	}
	cred, err := provider.GetCredentials()
	if err != nil {
		log.Fatal(err)
	}

	// 3. 使用腾讯云临时凭证
	fmt.Println(cred.SecretId)
	fmt.Println(cred.SecretKey)
	fmt.Println(cred.Token)
}
```

## 使用示例

以下示例即 [`samples/`](samples) 目录下的完整可运行代码。

### 腾讯云 SDK (tencentcloud-sdk-go)

列举 CLS 日志主题，见 [samples/list-cls-topics](samples/list-cls-topics)：

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

列举 COS 存储桶，见 [samples/list-cos-buckets](samples/list-cos-buckets)：

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

### 运行示例

示例从环境变量读取角色标识与地域，无需改代码即可对着真实租户跑：

```bash
export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json
# 交互式输入（无回显但有提示；zsh 语法。切勿把整块命令一起粘贴，
# 否则 read -s 会把下一行当成密钥吃掉）
read -rs "IDAAS_CLIENT_SECRET?输入 client 密钥: " && export IDAAS_CLIENT_SECRET
export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'
export TENCENT_CLOUD_REGION=ap-guangzhou              # 可选

go run ./samples/list-cls-topics     # CLS DescribeTopics
go run ./samples/list-cos-buckets    # COS Service.Get（列举 bucket）+ 后台轮换
```

两个示例都会按阶段打印 `[1/4]…[4/4]` 进度日志，所以"看起来卡住"时能直接看出卡在哪一段：
`[1/4]` 加载配置、`[2/4]` 构造提供者、`[3/4]` 调 PAM 换凭证（示例用 ctx 限制最长 30s）、
`[4/4]` 调腾讯云接口。

密钥输入用 `read -rs "变量名?提示: "`（zsh 写法）；光写 `read -s VAR` **没有提示**，
看起来就像终端死了，而且会把一起粘贴过来的下一行当成密钥吃掉。

未设置 `TENCENT_CLOUD_ROLE_ARN` 时会在调用 PAM 之前退出，并打印可直接复制的 `export` 语句。
CLS 示例使用地域化 endpoint `cls.<地域>.tencentcloudapi.com`。

### 自定义参数

也可以使用选项模式进行细粒度控制：

```go
provider, err := pam.NewIDaaSPamTencentCloudCredentialsProvider(
    pam.WithDeveloperApiEndpoint("https://developer-api.example.com"),
    pam.WithIdaasInstanceId("your-idaas-instance-id"),
    pam.WithOidcTokenProvider(yourOidcTokenProvider),
    pam.WithRoleArn("your-role-arn"),
    pam.WithConnectTimeout(5000), // 0 = 默认值；合法区间 [2000, 60000] ms
    pam.WithReadTimeout(10000),   // 0 = 默认值；合法区间 [2000, 60000] ms
    // 可选项：
    pam.WithStaleValueBehavior(enums.StaleValueBehaviorBlock),
    pam.WithHttpClient(sdkhttp.NewDefaultHttpClient(5000, 10000).WithTracePropagator(myPropagator)),
)
```

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `WithDeveloperApiEndpoint` | —（必填） | 需含协议前缀；便捷入口从核心 SDK 工厂自动获取 |
| `WithIdaasInstanceId` | —（必填） | 以 `url.PathEscape` 转义后拼入 URL path |
| `WithOidcTokenProvider` | —（必填） | `provider.OidcTokenProvider`；可桥接核心 SDK 凭证提供者，测试中可注入桩实现 |
| `WithRoleArn` | —（必填） | 作为 `cloudAccountRoleExternalId` query 参数发送 |
| `WithConnectTimeout` / `WithReadTimeout` | 5000 / 10000 ms | `0` 保持默认；越界在构造期拒绝（`ConnectTimeoutNotValid` / `ReadTimeoutNotValid`），与核心 SDK 配置校验同口径 |
| `WithStaleValueBehavior` | `REFRESH` | 取 `BLOCK` / `ALLOW` / `REFRESH`；空值归一化为 `REFRESH`，其余取值拒绝（`InvalidStaleValueBehavior`） |
| `WithHttpClient` | 核心 SDK 默认客户端 | 注入自定义客户端以支持代理、mTLS、自定义 TLS 或 trace 透传。注入后适配器不再自建客户端：超时、TLS、trace 全部由宿主客户端负责 |

### 配置生效范围

核心 SDK 的配置项并非都会影响本适配器，明确如下：

| 核心 SDK 配置 | 是否影响适配器 PAM 调用 | 控制方式 |
|---------------|--------------------------|----------|
| `httpConfiguration.connectTimeout` / `readTimeout` | **是**，经 `GetTencentCloudCredentialsProvider` | 也可用 `WithConnectTimeout` / `WithReadTimeout` 按 provider 覆盖 |
| `httpConfiguration.unsafeIgnoreSslCert` | **是**，仅经 `GetTencentCloudCredentialsProvider` | 适配器**不提供**任何关闭证书校验的选项；确有需要请用 `WithHttpClient` 自带客户端，把该决定明确留在自己代码里 |
| `staleValueBehavior` | **否** —— 核心 SDK beta.1 未暴露读取该字段的包级 API | 请用 `WithStaleValueBehavior`。注意即便配成 `ALLOW`，适配器仍拒绝交出已过期凭证（见下） |
| `factory.WithTracePropagator` | **否**（同样无 getter） | 注入 `sdkhttp.NewDefaultHttpClient(...).WithTracePropagator(p)` 构造的客户端即可 |

以下不变量与上述配置无关，始终成立：

- **绝不返回已过期凭证。** 若刷新失败而缓存凭证已真正过期，调用以 `CredentialExpired` fail-fast，而不是把过期凭证交下去、在下游变成一个难以定位根因的 403。
- **冷启动并发会放大调用。** 核心 SDK 在锁外读缓存、在锁内登记刷新 leader，两步不原子：16 并发首取实测最多 7 次 PAM 调用（理论上界即并发度）。这是**只在冷启动发生的一次性放大**，每个调用方拿到的仍是同一份有效凭证。稳态（缓存新鲜）下的并发读是**零** PAM 调用（已有单测钉死），因此不要把"每 N 个并发请求恰好 1 次调用"当作契约来断言或做限流依据。

## API 参考

### 工厂函数

| 函数 | 返回类型 | 说明 |
|------|---------|------|
| `GetTencentCloudCredentialsProvider(roleArn)` | `*IDaaSPamTencentCloudCredentialsProvider` | 创建腾讯云凭证提供者 |

### 凭证提供者方法

| 方法 | 返回类型 | 说明 |
|------|----------|------|
| `GetCredentials()` | `(*TencentCloudCredential, error)` | 返回缓存凭证，进入 stale 窗口时自动刷新。返回的是缓存凭证的**副本** —— 调用方改写它不会污染缓存 |
| `GetCredentialsWithContext(ctx)` | `(*TencentCloudCredential, error)` | 同上，但调用方的 `context` 参与凭证请求生命周期：取消/超时立即返回，不再干等在飞的刷新 |
| `Close()` | `error` | 释放 provider 持有的资源（空闲连接、预取策略）。幂等，调用后 provider 仍可继续使用 |
| `GetRoleArn()` / `GetIdaasInstanceId()` / `GetDeveloperApiEndpoint()` / `GetOIDCToken()` / `GetConnectTimeout()` / `GetReadTimeout()` | 各异 | 只读访问器，主要用于排障 |

> 自核心 SDK `v0.1.0-beta.1` 起：刷新失败时若仍在降级缓冲内会返回旧凭证；一旦旧凭证真正过期，
> provider 以错误码 `CredentialExpired` fail-fast，而不把过期凭证交给腾讯云 SDK。

### TencentCloudCredential

| 字段 | 类型 | 说明 |
|------|------|------|
| `SecretId` | `string` | 临时 SecretId |
| `SecretKey` | `string` | 临时 SecretKey |
| `Token` | `string` | 会话 Token |
| `Expiration` | `time.Time` | 凭证过期时间 |

### 错误码

| 错误码 | 含义 | 重试建议 |
|--------|------|----------|
| `InvalidParameter` | 必填选项缺失或为空 | 否 — 修正配置 |
| `ConnectTimeoutNotValid` / `ReadTimeoutNotValid` | 超时越出 `[2000, 60000]` ms | 否 — 修正取值 |
| `InvalidStaleValueBehavior` | `staleValueBehavior` 越出 `BLOCK` / `ALLOW` / `REFRESH` | 否 — 修正取值 |
| `OidcTokenError` | 从核心 SDK 取 OIDC Token 失败 | 可 — 检查核心 SDK 凭证/配置与端点连通性 |
| `HttpError` | 调用 PAM 的传输层失败 | 可 — 退避重试；仍有有效缓存时适配器会降级返回旧凭证 |
| `PamApiError` | PAM 返回非 2xx（消息含状态码与截断响应体） | 视状态码 |
| `ParseError` | PAM 响应字段缺失或结构不符 | 否 — 多为契约或服务端问题 |
| `CredentialExpired` | 缓存凭证已真正过期且刷新不可用 | PAM 恢复后可 — 适配器不会交出过期凭证 |

`ConnectTimeoutNotValid`、`ReadTimeoutNotValid`、`InvalidStaleValueBehavior` 直接复用核心 SDK 的码值（`idaas-go-core-sdk/constants`），便于上游统一按码归因。

### 环境变量

| 变量 | 说明 |
|------|------|
| `CLOUD_IDAAS_CONFIG_PATH` | IDaaS 配置文件路径 |

## 从 v0.1.0-alpha.3 升级

核心 SDK `v0.1.0-beta.1` 是破坏性版本，本适配器已完成迁移。

**会导致编译失败（必须改代码）：**
- `provider.OidcTokenProvider` 新增 `GetOidcTokenWithContext(ctx)`。自定义 OIDC provider 必须实现该方法 —— 便捷入口已为你桥接好核心 SDK 的 provider。
- `cache.NewCachedResultSupplier` 去掉了第三个 `prefetchThreshold` 参数；`cache.RefreshResult` 新增 `StaleTime` / `PrefetchTime`，并移除 `Error` 字段（错误改由刷新函数的 `error` 返回值承担）。
- `factory.GetInstance()` / `Initialize()` / `CreateCredentialProvider()` 已废弃（计划移除），改用包级 `factory.Init` / `InitWithConfig` / `GetIDaaSCredentialProvider`。

**需要知道的行为变化：**
- `GetCredentials()` 返回缓存凭证的**副本**；旧实现返回的是缓存内同一指针。
- 过期相关失败统一以 `CredentialExpired` 暴露（旧实现是 `ParseError` 或直接抛出缓存内部错误）；刷新失败的错误消息不再携带 2xx 原始响应体。
- 打印 `TencentCloudCredential`（`%v` / `%+v` / `%#v`）默认脱敏：`SecretKey` / `Token` 不再明文进日志；需要取值请显式读字段。
- 超时选项按 `[2000, 60000] ms` 校验；此前越界取值会被静默接受。

## 集成测试

真实环境测试放在 `integration` 构建标签后面，**默认不跑**（`go test ./...` 完全不会碰到它们）；
它们会访问 IDaaS token 端点、PAM Developer API，以及可选的腾讯云真实接口。

```bash
# 1) 配置：把 samples/cloud_idaas.json 复制成 <仓库根>/cloud_idaas.json（已被 gitignore）并填你自己的实例；
#    或用 CLOUD_IDAAS_CONFIG_PATH 指向任意路径
export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json
# 2) client 密钥：变量名由配置的 authnConfiguration.clientSecretEnvVarName 决定
export IDAAS_CLIENT_SECRET='<client 密钥>'
# 3) PAM 中配置的腾讯云账号角色外部标识
export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'

# 只验配置接线（不访问网络）：端点归一化、httpConfiguration 透传、instanceId/roleArn
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration_ConfigWiring -v

# 全链路（换取 STS + 真打 CLS 接口）
IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration -v -timeout 5m
```

| 开关 | 作用 |
|------|------|
| `IDAAS_INTEGRATION=on` | 总开关，非 `on` 一律 Skip |
| `TENCENT_API_PROBE=off` | 跳过真实 CLS `DescribeTopics` 探测（凭证链路仍跑） |
| `TENCENT_CLOUD_REGION` | 探测用的地域，默认 `ap-guangzhou` |
| `IDAAS_INTEGRATION_TIMEOUT` | 单次取凭证超时，默认 `30s` |

缺失项会直接给出可执行的补齐提示，而不是抛一个难以定位的网络错误。探测用例把
"已鉴权但被权限策略拒绝"（AccessDenied/UnauthorizedOperation）判为**通过**——它证明凭证与签名
已被腾讯云接受；`InvalidSecretId` / `InvalidToken` / `FailedToAuthenticate` 才判失败。
用例不会打印任何凭证明文（只输出 `redact()` 摘要）。

`cloud_idaas.json` 等本地配置/密钥文件已被 .gitignore 排除（仅根目录生效），避免真实租户配置入库；
`samples/cloud_idaas.json` 是可提交的占位模板，克隆后即可照着填。

## 支持与反馈

- **Issues 与反馈**：如有疑问或建议，请提交 [Issue](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter/issues)
- **安全漏洞披露**：见[安全策略](SECURITY.md)，按负责任披露流程报告。

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 许可证。
