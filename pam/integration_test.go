//go:build integration

// 集成测试：连接真实 IDaaS 环境与 PAM Developer API，端到端验证腾讯云 akless 链路
// （OIDC Token → PAM 换取腾讯云 STS → 真打一次腾讯云接口）。
//
// 默认不参与常规 `go test ./...`，需显式开启：
//
//	export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json   # 或把配置放仓库根 cloud_idaas.json
//	export IDAAS_CLIENT_SECRET='<client 密钥>'                     # 配置里 clientSecretEnvVarName 指向的名字
//	export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'
//	go test -tags=integration ./pam/ -run TestIntegration -v -timeout 5m
//
// 其中 TestIntegration_ConfigWiring 不访问网络，可先单独跑它确认"配置本身对不对"：
//
//	IDAAS_INTEGRATION=on go test -tags=integration ./pam/ -run TestIntegration_ConfigWiring -v
package pam

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	idaasconfig "github.com/cloud-idaas/idaas-go-core-sdk/config"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"

	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// integrationEnabled 集成测试开关。
func integrationEnabled() bool { return os.Getenv("IDAAS_INTEGRATION") == "on" }

// requireIntegration 前置校验：开关关闭则 Skip；开启则校验缺项、加载真实配置并重建工厂。
// 缺什么就 Fatal 出可执行的补齐指令，不让人猜。
func requireIntegration(t *testing.T) (*idaasconfig.IDaaSClientConfig, string) {
	t.Helper()

	if !integrationEnabled() {
		t.Skip("集成测试未开启：加 -tags=integration 并 export IDAAS_INTEGRATION=on")
	}

	roleArn := firstNonEmpty(os.Getenv("TENCENT_CLOUD_ROLE_ARN"), os.Getenv("IDAAS_ROLE_ARN"))
	if roleArn == "" {
		t.Fatal("缺 TENCENT_CLOUD_ROLE_ARN：PAM 中配置的腾讯云账号角色外部标识，" +
			"形如 qcs::cam::uin/<uin>:roleName/<role>（兼容旧名 IDAAS_ROLE_ARN）")
	}

	cfgPath := findLocalConfigPath()
	cfg, err := idaasconfig.NewConfigReader().LoadWithPriority(cfgPath)
	if err != nil {
		t.Fatalf("加载 IDaaS 配置失败（path=%q）：%v\n"+
			"请把真实配置放到仓库根 cloud_idaas.json，或 export CLOUD_IDAAS_CONFIG_PATH=/abs/path/client-config.json"+
			"（两者都不给则回退核心 SDK 默认链 ~/.cloud_idaas/client-config.json）", cfgPath, err)
	}

	if msg := missingCredentialInputs(cfg); msg != "" {
		t.Fatal(msg)
	}

	// 同包单测可能已把进程级工厂置为已初始化；先 Reset 再用真实配置重建，
	// 避免"改了配置看起来没生效"的误诊断。
	factory.Reset()
	if err := factory.InitWithConfig(cfg); err != nil {
		t.Fatalf("初始化核心 SDK 工厂失败：%v", err)
	}
	return cfg, roleArn
}

// missingCredentialInputs 把"还缺什么"折算成一句可执行提示。
func missingCredentialInputs(cfg *idaasconfig.IDaaSClientConfig) string {
	var missing []string
	if authn := cfg.AuthnConfiguration; authn != nil {
		if envName := authn.ClientSecretEnvVarName; envName != "" && os.Getenv(envName) == "" {
			missing = append(missing, fmt.Sprintf("export %s='<client 密钥>'（配置 clientSecretEnvVarName 指向它）", envName))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "集成测试还缺：\n  " + strings.Join(missing, "\n  ")
}

// findLocalConfigPath 定位本地真实配置：CLOUD_IDAAS_CONFIG_PATH > 仓库根 cloud_idaas.json >
// 包目录 cloud_idaas.json > 空串（交核心 SDK 默认链）。
// go test 的工作目录是包目录 pam/ 而非仓库根，必须按测试文件位置定位，否则会静默回退到
// ~/.cloud_idaas 下的旧配置。
func findLocalConfigPath() string {
	if p := os.Getenv("CLOUD_IDAAS_CONFIG_PATH"); p != "" {
		return p
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	for _, c := range []string{
		filepath.Join(filepath.Dir(thisFile), "..", "cloud_idaas.json"),
		filepath.Join(filepath.Dir(thisFile), "cloud_idaas.json"),
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// redact 输出"前缀 + 长度 + SHA256 前 8 位"，可跨行关联又不把凭证原文送进测试日志。
func redact(s string) string {
	if s == "" {
		return "(empty)"
	}
	prefix := s
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%s**(len=%d,sha256=%x)", prefix, len(s), sum[:4])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func integrationTimeout() time.Duration {
	if v := os.Getenv("IDAAS_INTEGRATION_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// TestIntegration_ConfigWiring 只验证配置到适配器的接线，不访问网络：
// 可以先单独跑它，确认"配置本身写对了没有"。
func TestIntegration_ConfigWiring(t *testing.T) {
	cfg, roleArn := requireIntegration(t)

	provider, err := GetTencentCloudCredentialsProvider(roleArn)
	if err != nil {
		t.Fatalf("GetTencentCloudCredentialsProvider: %v", err)
	}
	defer provider.Close()

	// 裸 host 必须被核心 SDK 归一化成 https://，否则适配器拼出的 PAM URL 没有协议、请求必失败。
	if ep := provider.GetDeveloperApiEndpoint(); !strings.HasPrefix(ep, "https://") {
		t.Errorf("developerApiEndpoint 未归一化为 https：got %q（配置原值 %q）", ep, cfg.DeveloperApiEndpoint)
	} else {
		t.Logf("developerApiEndpoint = %s", ep)
	}

	// 租户级 httpConfiguration 必须真的作用到适配器。
	if hc := cfg.HttpConfiguration; hc != nil {
		if hc.ConnectTimeout != 0 && provider.GetConnectTimeout() != hc.ConnectTimeout {
			t.Errorf("connectTimeout 未透传：provider=%d config=%d", provider.GetConnectTimeout(), hc.ConnectTimeout)
		}
		if hc.ReadTimeout != 0 && provider.GetReadTimeout() != hc.ReadTimeout {
			t.Errorf("readTimeout 未透传：provider=%d config=%d", provider.GetReadTimeout(), hc.ReadTimeout)
		}
	}
	t.Logf("生效超时 connect=%dms read=%dms；instanceId=%s roleArn=%s",
		provider.GetConnectTimeout(), provider.GetReadTimeout(), provider.GetIdaasInstanceId(), provider.GetRoleArn())

	if provider.GetIdaasInstanceId() != cfg.InstanceId {
		t.Errorf("instanceId 不一致：provider=%q config=%q", provider.GetIdaasInstanceId(), cfg.InstanceId)
	}
	if provider.GetRoleArn() != roleArn {
		t.Errorf("roleArn 不一致：provider=%q want=%q", provider.GetRoleArn(), roleArn)
	}
	if provider.GetOIDCToken() != "" {
		t.Errorf("新建 provider 不应已有 OIDC Token，got %q", provider.GetOIDCToken())
	}
}

// TestIntegration_ObtainTencentCredential 全链路：核心 SDK 取 OIDC Token →
// PAM Developer API 换腾讯云 STS → 字段完整、未过期、二次调用命中缓存。
func TestIntegration_ObtainTencentCredential(t *testing.T) {
	_, roleArn := requireIntegration(t)

	provider, err := GetTencentCloudCredentialsProvider(roleArn)
	if err != nil {
		t.Fatalf("GetTencentCloudCredentialsProvider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout())
	defer cancel()

	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		t.Fatalf("GetCredentialsWithContext 失败：%v", err)
	}
	if cred.GetSecretId() == "" || cred.GetSecretKey() == "" || cred.GetToken() == "" {
		t.Fatalf("PAM 返回的凭证字段不完整：%+v", cred)
	}
	if !cred.GetExpiration().After(time.Now()) {
		t.Errorf("PAM 返回的凭证已过期：expiration=%s", cred.GetExpiration())
	}
	t.Logf("SecretId=%s SecretKey=%s Token=%s expiration=%s",
		redact(cred.GetSecretId()), redact(cred.GetSecretKey()), redact(cred.GetToken()), cred.GetExpiration())

	// 直接打印凭证也不得出现明文（脱敏 String 的端到端验证）。
	if strings.Contains(fmt.Sprintf("%+v", cred), cred.GetSecretKey()) {
		t.Error("凭证被 fmt 明文打印，String 脱敏失效（见 domain.TencentCloudCredential.String）")
	}
	if provider.GetOIDCToken() == "" {
		t.Error("刷新后应记录最近一次使用的 OIDC Token")
	}

	again, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		t.Fatalf("第二次 GetCredentialsWithContext 失败：%v", err)
	}
	if again.GetSecretId() != cred.GetSecretId() {
		t.Error("缓存未命中：两次拿到不同凭证（PAM 被重复调用）")
	}
}

// TestIntegration_CallerContextBoundsWait 调用方 ctx 必须参与凭证等待：取消即返回，不干等。
func TestIntegration_CallerContextBoundsWait(t *testing.T) {
	_, roleArn := requireIntegration(t)

	provider, err := GetTencentCloudCredentialsProvider(roleArn)
	if err != nil {
		t.Fatalf("GetTencentCloudCredentialsProvider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	cred, err := provider.GetCredentialsWithContext(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("已取消的 ctx 不应取到凭证，got %+v", cred)
	}
	if cred != nil {
		t.Errorf("已取消的 ctx 却返回了凭证：%+v", cred)
	}
	if elapsed > 5*time.Second {
		t.Errorf("ctx 取消后仍在干等刷新：耗时 %s", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("取消路径的错误形态（非 context.Canceled，请人工确认）：%T %v", err, err)
	} else {
		t.Logf("ctx 取消在 %s 内返回：%v", elapsed, err)
	}
}

// TestIntegration_TencentCloudApiProbe 用换来的 STS 真打一次腾讯云接口（CLS DescribeTopics）。
//
// 判定口径：只要请求被腾讯云受理并返回业务错误，就证明 akless 凭证 + 签名链路端到端可用，
// 权限不足（AccessDenied / UnauthorizedOperation 等）视为通过；凭证本身有问题会是另一种形态
// （InvalidSecretId / InvalidToken / FailedToAuthenticate），判失败；网络层错误无法下结论，判失败。
// 想跳过：export TENCENT_API_PROBE=off。
func TestIntegration_TencentCloudApiProbe(t *testing.T) {
	if os.Getenv("TENCENT_API_PROBE") == "off" {
		t.Skip("TENCENT_API_PROBE=off，跳过真实腾讯云接口探测")
	}
	_, roleArn := requireIntegration(t)

	provider, err := GetTencentCloudCredentialsProvider(roleArn)
	if err != nil {
		t.Fatalf("GetTencentCloudCredentialsProvider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout())
	defer cancel()

	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		t.Fatalf("获取凭证失败：%v", err)
	}

	region := firstNonEmpty(os.Getenv("TENCENT_CLOUD_REGION"), "ap-guangzhou")
	httpProfile := profile.NewHttpProfile()
	httpProfile.Endpoint = fmt.Sprintf("cls.%s.tencentcloudapi.com", region)
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile = httpProfile

	client, err := cls.NewClient(common.NewTokenCredential(cred.GetSecretId(), cred.GetSecretKey(), cred.GetToken()), region, clientProfile)
	if err != nil {
		t.Fatalf("构建 CLS 客户端失败：%v", err)
	}

	resp, err := client.DescribeTopics(cls.NewDescribeTopicsRequest())
	if err == nil {
		t.Logf("CLS DescribeTopics 成功：返回 %d 个 topic（region=%s）", len(resp.Response.Topics), region)
		return
	}

	msg := err.Error()
	switch {
	case containsAny(msg, "InvalidSecretId", "InvalidToken", "ExpiredToken", "FailedToAuthenticate",
		"SignatureDoesNotMatch", "InvalidKey", "AuthFailure"):
		t.Errorf("凭证/签名被腾讯云拒绝，链路有问题：%s", msg)
	case containsAny(msg, "AccessDenied", "UnauthorizedOperation", "NoPermission", "not authorized",
		"permission", "denied", "Forbidden"):
		t.Logf("已通过身份认证、仅被权限策略拒绝 ⇒ akless 链路端到端可用：%s", msg)
	case containsAny(msg, "dial tcp", "no such host", "connection refused", "i/o timeout", "x509", "TLS"):
		t.Errorf("没打到腾讯云（网络/传输层问题），无法判定链路：%s", msg)
	default:
		t.Logf("腾讯云返回业务错误（身份链路已通，语义请人工确认）：%s", msg)
	}
}
