# idaas-go-akless-tencent-adapter

[![Go Version](https://img.shields.io/badge/go-1.18%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Development Status](https://img.shields.io/badge/status-Beta-orange)](https://github.com/aliyun/idaas-go-akless-tencent-adapter)
[![Version](https://img.shields.io/badge/version-0.0.1--beta-blue)](https://github.com/aliyun/idaas-go-akless-tencent-adapter)

[English](README.md) | 简体中文

IDaaS（身份即服务）AKless 适配器 Go SDK —— 通过 IDaaS PAM（特权访问管理）获取 STS 临时凭证，无需 SecretKey 即可访问腾讯云服务。

## 特性

- **免 AK 认证**：无需长期 SecretKey。通过 OIDC Token 经 IDaaS PAM 获取 STS 临时凭证，降低凭证泄露风险
- **多 SDK 适配**：提供腾讯云 SDK 凭证提供者，同时兼容 COS SDK
- **凭证自动刷新**：内置凭证缓存和基于过期时间的自动刷新机制，确保凭证无缝轮转
- **易于集成**：工厂函数提供一行代码创建凭证提供者，最大程度降低集成成本

## 环境要求

- Go >= 1.18
- 依赖：
  - idaas-go-core-sdk >= 0.0.6

## 安装

```bash
go get github.com/aliyun/idaas-go-akless-tencent-adapter
```

## 前置条件

本 SDK 依赖 [idaas-go-core-sdk](https://github.com/aliyun/idaas-go-core-sdk)。使用本适配器前，需要完成 IDaaS Core SDK 的初始化。

1. 添加 `idaas-go-core-sdk` 依赖并完成配置。详见 [idaas-go-core-sdk README](https://github.com/aliyun/idaas-go-core-sdk/blob/main/README.md)。

2. 在配置文件中，将 `scope` 设置为 IDaaS PAM 内置 scope：

   ```json
   {
       "scope": "urn:cloud:idaas:pam|cloud_account_role:obtain_access_credential"
   }
   ```

3. 完成 IDaaS Core SDK 初始化：

   ```go
   import (
       "github.com/aliyun/idaas-go-core-sdk/config"
       "github.com/aliyun/idaas-go-core-sdk/factory"
   )

   func main() {
       cfg, _ := config.LoadConfig("")
       factory.GetInstance().Initialize(cfg)
   }
   ```

## 快速开始

最简单的使用方式是通过工厂函数：

```go
package main

import (
    "fmt"
    "log"

    "github.com/aliyun/idaas-go-core-sdk/config"
    "github.com/aliyun/idaas-go-core-sdk/factory"
    "github.com/aliyun/idaas-go-akless-tencent-adapter/pam"
)

func main() {
    // 1. 初始化 IDaaS Core SDK
    cfg, err := config.LoadConfig("")
    if err != nil {
        log.Fatal(err)
    }
    if err := factory.GetInstance().Initialize(cfg); err != nil {
        log.Fatal(err)
    }

    // 2. 创建腾讯云凭证提供者
    provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // 3. 获取凭证
    credential, err := provider.GetCredentials()
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(credential.SecretId)
    fmt.Println(credential.SecretKey)
    fmt.Println(credential.Token)
}
```

## 使用示例

### 腾讯云 SDK (tencentcloud-sdk-go)

```go
package main

import (
    "log"

    "github.com/aliyun/idaas-go-core-sdk/config"
    "github.com/aliyun/idaas-go-core-sdk/factory"
    "github.com/aliyun/idaas-go-akless-tencent-adapter/pam"
    "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
    "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
    cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
)

func main() {
    // 初始化
    cfg, _ := config.LoadConfig("")
    factory.GetInstance().Initialize(cfg)

    // 创建腾讯云凭证提供者
    provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // 获取凭证并用于腾讯云 SDK
    cred, err := provider.GetCredentials()
    if err != nil {
        log.Fatal(err)
    }

    credential := common.NewTokenCredential(cred.SecretId, cred.SecretKey, cred.Token)
    cpf := profile.NewClientProfile()
    client, _ := cvm.NewClient(credential, "ap-guangzhou", cpf)
    _ = client
}
```

### COS (cos-go-sdk-v5)

```go
package main

import (
    "context"
    "log"
    "net/http"
    "net/url"

    "github.com/aliyun/idaas-go-core-sdk/config"
    "github.com/aliyun/idaas-go-core-sdk/factory"
    "github.com/aliyun/idaas-go-akless-tencent-adapter/pam"
    "github.com/tencentyun/cos-go-sdk-v5"
)

func main() {
    // 初始化
    cfg, _ := config.LoadConfig("")
    factory.GetInstance().Initialize(cfg)

    // 创建凭证提供者
    provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // 获取凭证并用于 COS SDK
    cred, _ := provider.GetCredentials()

    u, _ := url.Parse("https://your-bucket.cos.ap-guangzhou.myqcloud.com")
    b := &cos.BaseURL{BucketURL: u}
    client := cos.NewClient(b, &http.Client{
        Transport: &cos.AuthorizationTransport{
            SecretID:     cred.SecretId,
            SecretKey:    cred.SecretKey,
            SessionToken: cred.Token,
        },
    })

    // 列出对象
    result, _, _ := client.Bucket.Get(context.Background(), nil)
    _ = result
}
```

### 自定义参数

也可以使用选项模式进行细粒度控制：

```go
provider, err := pam.NewIDaaSPamTencentCloudCredentialsProvider(
    pam.WithDeveloperApiEndpoint("https://developer-api.example.com"),
    pam.WithIdaasInstanceId("your-idaas-instance-id"),
    pam.WithOidcTokenProvider(yourOidcTokenProvider),
    pam.WithRoleArn("your-role-arn"),
    pam.WithConnectTimeout(5000),
    pam.WithReadTimeout(10000),
)
```

## API 参考

### 工厂函数

| 函数 | 返回类型 | 说明 |
|------|---------|------|
| `GetTencentCloudCredentialsProvider(roleArn)` | `*IDaaSPamTencentCloudCredentialsProvider` | 创建腾讯云凭证提供者 |

### TencentCloudCredential

| 字段 | 类型 | 说明 |
|------|------|------|
| `SecretId` | `string` | 临时 SecretId |
| `SecretKey` | `string` | 临时 SecretKey |
| `Token` | `string` | 会话 Token |
| `Expiration` | `time.Time` | 凭证过期时间 |

### 环境变量

| 变量 | 说明 |
|------|------|
| `CLOUD_IDAAS_CONFIG_PATH` | IDaaS 配置文件路径 |

## 支持与反馈

- **邮箱**: cloudidaas@list.alibaba-inc.com
- **Issues**: 如有疑问或建议，请提交 [Issue](https://github.com/aliyun/idaas-go-akless-tencent-adapter/issues)

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 许可证。
