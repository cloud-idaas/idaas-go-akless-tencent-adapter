# idaas-go-akless-tencent-adapter

[![Go Version](https://img.shields.io/badge/go-1.18%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Development Status](https://img.shields.io/badge/status-Beta-orange)](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter)
[![Version](https://img.shields.io/badge/version-0.1.0--beta.1-blue)](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter)

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
  - idaas-go-core-sdk >= 0.1.0-alpha.3

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
       "github.com/cloud-idaas/idaas-go-core-sdk/config"
       "github.com/cloud-idaas/idaas-go-core-sdk/factory"
   )

   func main() {
       reader := config.NewConfigReader()
       cfg, err := reader.LoadWithPriority("")
       if err != nil {
           panic(err)
       }
       factoryInstance := factory.GetInstance()
       err = factoryInstance.Initialize(cfg)
       if err != nil {
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
	"github.com/cloud-idaas/idaas-go-core-sdk/config"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
)

func main() {
	// 1. 初始化 IDaaS Core SDK
	reader := config.NewConfigReader()
	cfg, err := reader.LoadWithPriority("")
	if err != nil {
		panic(err)
	}
	factoryInstance := factory.GetInstance()
	err = factoryInstance.Initialize(cfg)
	if err != nil {
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
	"fmt"
	"log"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/config"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

func main() {
	// 1. 初始化 IDaaS Core SDK
	reader := config.NewConfigReader()
	cfg, err := reader.LoadWithPriority("")
	if err != nil {
		panic(err)
	}
	factoryInstance := factory.GetInstance()
	err = factoryInstance.Initialize(cfg)
	if err != nil {
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

	// 3. 使用腾讯云凭证创建腾讯云客户端
	region := "ap-guangzhou"
	credential := common.NewTokenCredential(cred.SecretId, cred.SecretKey, cred.Token)
	httpProfile := profile.NewHttpProfile()
	httpProfile.Endpoint = "cls.tencentcloudapi.com"
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile = httpProfile
	client, err := cls.NewClient(credential, region, clientProfile)
	if err != nil {
		log.Fatal(err)
	}

	// 4. 使用腾讯云客户端调用腾讯云 API
	request := cls.NewDescribeTopicsRequest()
	response, err := client.DescribeTopics(request)
	if err != nil {
		log.Fatal(err)
	}
	for _, topic := range response.Response.Topics {
		fmt.Printf("TopicId: %s | TopicName: %s | LogsetId: %s\n",
			*topic.TopicId, *topic.TopicName, *topic.LogsetId)
	}
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

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/config"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	"github.com/tencentyun/cos-go-sdk-v5"
)

func main() {
	// 1. 初始化 IDaaS Core SDK
	reader := config.NewConfigReader()
	cfg, err := reader.LoadWithPriority("")
	if err != nil {
		panic(err)
	}
	factoryInstance := factory.GetInstance()
	err = factoryInstance.Initialize(cfg)
	if err != nil {
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

	// 3. 使用腾讯云凭证创建腾讯云客户端
	u, _ := url.Parse(fmt.Sprintf("https://cos.%s.myqcloud.com", "ap-guangzhou"))
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:     cred.SecretId,
			SecretKey:    cred.SecretKey,
			SessionToken: cred.Token,
		},
	})

	// 4. 使用腾讯云客户端调用腾讯云 API
	result, _, err := client.Service.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, bucket := range result.Buckets {
		fmt.Printf("Bucket: %s | Region: %s | CreateDate: %s\n",
			bucket.Name, bucket.Region, bucket.CreationDate)
	}
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
- **Issues**: 如有疑问或建议，请提交 [Issue](https://github.com/cloud-idaas/idaas-go-akless-tencent-adapter/issues)

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 许可证。
