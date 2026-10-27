package pam

import (
	"context"

	adapterconstants "github.com/cloud-idaas/idaas-go-akless-tencent-adapter/constants"
	sdkerrors "github.com/cloud-idaas/idaas-go-core-sdk/errors"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	"github.com/cloud-idaas/idaas-go-core-sdk/provider"
)

// oidcTokenAdapter 将核心 SDK 的 IDaaSCredentialProvider 适配为 OidcTokenProvider。
//
// 核心 SDK 的 IDaaSCredentialProvider.GetCredential() 返回 IDaaSCredential，
// 其中的 AccessToken 即作为访问 PAM Developer API 的 Bearer Token。
// 这与 idaas-go-akless-aws-adapter / idaas-go-akless-alibabacloud-adapter 的桥接逻辑一致。
type oidcTokenAdapter struct {
	credProvider provider.IDaaSCredentialProvider
}

// 编译期断言：核心 SDK v0.1.0-beta.1 的 OidcTokenProvider 新增了
// GetOidcTokenWithContext 方法（Go 无默认方法，属破坏性变更），此处固化实现。
var _ provider.OidcTokenProvider = (*oidcTokenAdapter)(nil)

// GetOidcToken 实现 provider.OidcTokenProvider，等价于
// GetOidcTokenWithContext(context.Background())。
func (a *oidcTokenAdapter) GetOidcToken() (string, error) {
	return a.GetOidcTokenWithContext(context.Background())
}

// GetOidcTokenWithContext 实现 provider.OidcTokenProvider。
//
// ctx 透传到 IDaaSCredentialProvider.GetCredentialWithContext，使核心 SDK 内的
// Token Endpoint / 元数据服务调用同样受刷新链路 ctx 的取消/超时约束，而不必等其
// 各自走到连接、读取超时上限。
func (a *oidcTokenAdapter) GetOidcTokenWithContext(ctx context.Context) (string, error) {
	cred, err := a.credProvider.GetCredentialWithContext(ctx)
	if err != nil {
		return "", err
	}
	return cred.GetAccessToken(), nil
}

// GetTencentCloudCredentialsProvider 创建腾讯云凭证提供者（便捷入口）。
//
// 仅需传入 roleArn，其余参数（developerApiEndpoint / idaasInstanceId / credentialProvider）
// 从已初始化的核心 SDK 工厂自动获取，需先调用 factory.Init / factory.InitWithConfig。
// 多实例等高级场景请用全参构造器 NewIDaaSPamTencentCloudCredentialsProvider(With*...)。
//
// 走核心 SDK 的包级稳定 API（factory.GetIDaaSCredentialProvider /
// factory.GetDeveloperApiEndpoint / factory.GetIDaaSInstanceId），不依赖已 Deprecated 的
// GetInstance / CreateCredentialProvider——二者保留为薄壳，后续小版本将移除，适配层须提前
// 脱钩以免核心 SDK 升级时编译失败。未初始化时 GetIDaaSCredentialProvider 返回 NotInit 错误
// 并透传（旧写法 GetConfig() 返回 nil 会在此处空指针 panic）。
func GetTencentCloudCredentialsProvider(roleArn string) (*IDaaSPamTencentCloudCredentialsProvider, error) {
	credProvider, err := factory.GetIDaaSCredentialProvider()
	if err != nil {
		return nil, err
	}

	endpoint := factory.GetDeveloperApiEndpoint()
	instanceId := factory.GetIDaaSInstanceId()
	if endpoint == "" || instanceId == "" {
		return nil, sdkerrors.NewConfigError(adapterconstants.InvalidParameter,
			"core SDK factory config missing developerApiEndpoint or idaasInstanceId, call factory.Init() first", nil)
	}

	opts := []CredentialsProviderOption{
		WithOidcTokenProvider(&oidcTokenAdapter{credProvider: credProvider}),
		WithDeveloperApiEndpoint(endpoint),
		WithIdaasInstanceId(instanceId),
		WithRoleArn(roleArn),
	}

	// 租户级 httpConfiguration 一并透传：超时与 unsafeIgnoreSslCert 此前只作用于核心 SDK
	// 自身的 token 请求，适配器自建客户端把它完全忽略，导致"调大超时/内网自签证书"在
	// PAM 这一段不生效（典型症状：取 token 成功、换 STS 直接 x509 失败）。
	// 透传口径与核心 SDK factory 构造自己的客户端时完全一致。
	//
	// unsafeIgnoreSslCert 只在便捷入口这一条链路透传，且不构成绕过：宿主
	// factory.WithTLSEnforcement() 时该值 true 会在 Initialize 阶段被硬拒
	// （InsecureTLSDisallowed），走到这里时必然是 false；适配器不对外提供跳过校验的选项。
	if hc := factory.GetHttpConfig(); hc != nil {
		opts = append(opts,
			WithConnectTimeout(hc.ConnectTimeout),
			WithReadTimeout(hc.ReadTimeout),
			withUnsafeIgnoreSSLCert(hc.UnsafeIgnoreSSLCert),
		)
	}

	// 已知的核心 SDK API 缺口（非本分支可解；值得给核心 SDK 提包级 getter）：
	//   - staleValueBehavior：配置字段存在（beta 标记 Experimental），但只有 Deprecated 的
	//     GetInstance().GetConfig() 能读到，包级无 getter → 无法在此透传，
	//     适配器保持默认 REFRESH，需要别的策略请用 WithStaleValueBehavior（见 README）。
	//   - tracePropagator：factory.WithTracePropagator 注入的透传器只作用于核心 SDK 出站调用，
	//     同样无 getter。需要 akless 段链路连续，请用 WithHttpClient 传入
	//     sdkhttp.NewDefaultHttpClient(...).WithTracePropagator(p) 自建客户端。
	return NewIDaaSPamTencentCloudCredentialsProvider(opts...)
}
