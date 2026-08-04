# idaas-go-akless-tencent-adapter

[![Go Version](https://img.shields.io/badge/go-1.18%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)
[![Development Status](https://img.shields.io/badge/status-Beta-orange)](https://github.com/aliyun/idaas-go-akless-tencent-adapter)
[![Version](https://img.shields.io/badge/version-0.0.1--beta-blue)](https://github.com/aliyun/idaas-go-akless-tencent-adapter)

English | [简体中文](README_zh.md)

Go SDK for the IDaaS (Identity as a Service) AKless Adapter — obtain STS temporary credentials through IDaaS PAM (Privileged Access Management) to access Tencent Cloud services without SecretKey.

## Features

- **AK-Free Authentication**: No long-term SecretKey required. Uses OIDC Token to obtain STS temporary credentials via IDaaS PAM, reducing the risk of credential leakage
- **Multi-SDK Adaptation**: Provides credential provider for Tencent Cloud SDK, also compatible with COS SDK
- **Auto Credential Refresh**: Built-in credential caching and expiration-based auto-refresh mechanism to ensure seamless credential rotation
- **Easy Integration**: Factory functions provide one-line credential provider creation, minimizing integration cost

## Requirements

- Go >= 1.18
- Dependencies:
  - idaas-go-core-sdk >= 0.0.6

## Installation

```bash
go get github.com/aliyun/idaas-go-akless-tencent-adapter
```

## Prerequisites

This SDK depends on [idaas-go-core-sdk](https://github.com/aliyun/idaas-go-core-sdk). You need to complete the IDaaS Core SDK initialization before using this adapter.

1. Add the `idaas-go-core-sdk` dependency and complete the configuration. See [idaas-go-core-sdk README](https://github.com/aliyun/idaas-go-core-sdk/blob/main/README.md) for details.

2. In the configuration file, set the `scope` to the IDaaS PAM built-in scope:

   ```json
   {
       "scope": "urn:cloud:idaas:pam|cloud_account_role:obtain_access_credential"
   }
   ```

3. Complete the IDaaS Core SDK initialization:

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

## Quick Start

The simplest way to use this SDK is through the factory functions:

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
    // 1. Initialize IDaaS Core SDK
    cfg, err := config.LoadConfig("")
    if err != nil {
        log.Fatal(err)
    }
    if err := factory.GetInstance().Initialize(cfg); err != nil {
        log.Fatal(err)
    }

    // 2. Create Tencent Cloud credentials provider
    provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // 3. Get credentials
    credential, err := provider.GetCredentials()
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(credential.SecretId)
    fmt.Println(credential.SecretKey)
    fmt.Println(credential.Token)
}
```

## Usage Examples

### Tencent Cloud SDK (tencentcloud-sdk-go)

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
    // Initialize
    cfg, _ := config.LoadConfig("")
    factory.GetInstance().Initialize(cfg)

    // Create Tencent Cloud credentials provider
    provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // Get credentials and use with Tencent Cloud SDK
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
    // Initialize
    cfg, _ := config.LoadConfig("")
    factory.GetInstance().Initialize(cfg)

    // Create COS credentials provider
    cosProvider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
    if err != nil {
        log.Fatal(err)
    }

    // Get credentials and use with COS SDK
    cred, _ := cosProvider.GetCredentials()

    u, _ := url.Parse("https://your-bucket.cos.ap-guangzhou.myqcloud.com")
    b := &cos.BaseURL{BucketURL: u}
    client := cos.NewClient(b, &http.Client{
        Transport: &cos.AuthorizationTransport{
            SecretID:     cred.SecretId,
            SecretKey:    cred.SecretKey,
            SessionToken: cred.Token,
        },
    })
    _ = client

    // List objects
    result, _, _ := client.Bucket.Get(context.Background(), nil)
    _ = result
}
```

### Custom Parameters

You can use the options pattern for fine-grained control:

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

## API Reference

### Factory Functions

| Function | Return Type | Description |
|----------|-------------|-------------|
| `GetTencentCloudCredentialsProvider(roleArn)` | `*IDaaSPamTencentCloudCredentialsProvider` | Create a Tencent Cloud credentials provider |

### TencentCloudCredential

| Field | Type | Description |
|-------|------|-------------|
| `SecretId` | `string` | Temporary SecretId |
| `SecretKey` | `string` | Temporary SecretKey |
| `Token` | `string` | Session Token |
| `Expiration` | `time.Time` | Credential expiration time |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `CLOUD_IDAAS_CONFIG_PATH` | IDaaS configuration file path |

## Support & Feedback

- **Email**: cloudidaas@list.alibaba-inc.com
- **Issues**: For questions or suggestions, please submit an [Issue](https://github.com/aliyun/idaas-go-akless-tencent-adapter/issues)

## License

This project is licensed under the [Apache License 2.0](LICENSE).
