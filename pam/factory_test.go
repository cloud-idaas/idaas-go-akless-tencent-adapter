package pam

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	adapterconstants "github.com/cloud-idaas/idaas-go-akless-tencent-adapter/constants"
	idaasconfig "github.com/cloud-idaas/idaas-go-core-sdk/config"
	sdkconstants "github.com/cloud-idaas/idaas-go-core-sdk/constants"
	"github.com/cloud-idaas/idaas-go-core-sdk/credential"
	"github.com/cloud-idaas/idaas-go-core-sdk/enums"
	sdkerrors "github.com/cloud-idaas/idaas-go-core-sdk/errors"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	"github.com/cloud-idaas/idaas-go-core-sdk/provider"
)

// --- fakes ---

// fakeCredential 最小化 credential.IDaaSCredential 实现（适配器只关心 AccessToken）。
type fakeCredential struct {
	accessToken string
}

func (c *fakeCredential) GetAccessToken() string  { return c.accessToken }
func (c *fakeCredential) GetTokenType() string    { return "Bearer" }
func (c *fakeCredential) GetExpiresIn() int64     { return 3600 }
func (c *fakeCredential) GetScope() string        { return "openid" }
func (c *fakeCredential) GetRefreshToken() string { return "" }
func (c *fakeCredential) String() string          { return "[redacted]" }

var _ credential.IDaaSCredential = (*fakeCredential)(nil)

// stubCredentialProvider 实现 provider.IDaaSCredentialProvider，记录最近一次 ctx。
type stubCredentialProvider struct {
	cred credential.IDaaSCredential
	err  error

	mu      sync.Mutex
	calls   int
	lastCtx context.Context
}

func (s *stubCredentialProvider) GetCredential() (credential.IDaaSCredential, error) {
	return s.GetCredentialWithContext(context.Background())
}

// GetCredentialWithContext 实现 provider.IDaaSCredentialProvider（v0.1.0-beta.1 新增方法）。
func (s *stubCredentialProvider) GetCredentialWithContext(ctx context.Context) (credential.IDaaSCredential, error) {
	s.mu.Lock()
	s.calls++
	s.lastCtx = ctx
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.cred, nil
}

var _ provider.IDaaSCredentialProvider = (*stubCredentialProvider)(nil)

// --- OIDC 桥接（core SDK v0.1.0-beta.1 起 OidcTokenProvider 要求带 ctx 方法）---

func TestOidcTokenAdapter_GetOidcToken(t *testing.T) {
	stub := &stubCredentialProvider{cred: &fakeCredential{accessToken: "access-token-1"}}
	adapter := &oidcTokenAdapter{credProvider: stub}

	got, err := adapter.GetOidcToken()
	if err != nil {
		t.Fatalf("GetOidcToken failed: %v", err)
	}
	if got != "access-token-1" {
		t.Fatalf("GetOidcToken = %q, want access-token-1", got)
	}

	stub.mu.Lock()
	calls, lastCtx := stub.calls, stub.lastCtx
	stub.mu.Unlock()
	if calls != 1 || lastCtx == nil {
		t.Fatalf("GetOidcToken must delegate to GetCredentialWithContext with non-nil ctx (calls=%d ctxNil=%v)", calls, lastCtx == nil)
	}
}

func TestOidcTokenAdapter_GetOidcTokenWithContext_PassesCtx(t *testing.T) {
	stub := &stubCredentialProvider{cred: &fakeCredential{accessToken: "tok"}}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-1")

	got, err := (&oidcTokenAdapter{credProvider: stub}).GetOidcTokenWithContext(ctx)
	if err != nil || got != "tok" {
		t.Fatalf("GetOidcTokenWithContext = (%q, %v)", got, err)
	}

	stub.mu.Lock()
	lastCtx := stub.lastCtx
	stub.mu.Unlock()
	if v := lastCtx.Value(ctxKey{}); v != "trace-1" {
		t.Fatalf("caller ctx not propagated to IDaaSCredentialProvider: got %v", v)
	}
}

func TestOidcTokenAdapter_ErrorPassthrough(t *testing.T) {
	want := errors.New("token endpoint unavailable")
	stub := &stubCredentialProvider{err: want}

	if _, err := (&oidcTokenAdapter{credProvider: stub}).GetOidcToken(); !errors.Is(err, want) {
		t.Fatalf("GetOidcToken error = %v, want it to wrap %v", err, want)
	}
	if _, err := (&oidcTokenAdapter{credProvider: stub}).GetOidcTokenWithContext(context.Background()); !errors.Is(err, want) {
		t.Fatalf("GetOidcTokenWithContext error = %v, want it to wrap %v", err, want)
	}
}

// --- 便捷工厂入口（走核心 SDK 包级稳定 API，不依赖 Deprecated 的 GetInstance/CreateCredentialProvider）---

func TestGetTencentCloudCredentialsProvider_RequiresInitializedFactory(t *testing.T) {
	// 工厂是进程级单例：进入前与结束后各 Reset 一次，保证用例之间互不影响。
	factory.Reset()
	t.Cleanup(factory.Reset)

	p, err := GetTencentCloudCredentialsProvider("qcs::cam::uin/100000:roleName/demo")
	if p != nil {
		t.Fatal("expected nil provider when core SDK factory is not initialised")
	}
	// 旧写法 GetInstance().GetConfig() 在未初始化时会返回 nil 配置并在取字段时空指针 panic；
	// 改用包级 API 后这里必须是可预期的 ConfigError。
	var cfgErr *sdkerrors.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *sdkerrors.ConfigError, got %T: %v", err, err)
	}
	if cfgErr.Code != sdkconstants.IDaaSCredentialProviderFactoryNotInit {
		t.Fatalf("error code = %s, want %s", cfgErr.Code, sdkconstants.IDaaSCredentialProviderFactoryNotInit)
	}
}

// TestGetTencentCloudCredentialsProvider_FactoryMissingEndpoint 覆盖 factory.go 中
// "工厂已初始化但 developerApiEndpoint/idaasInstanceId 为空"的防御分支。
//
// developerApiEndpoint 在核心 SDK 校验器里是可选字段（仅非空时强制 https），因此一个只配了
// token exchange、未配 PAM endpoint 的配置能通过 Initialize，工厂进入 initialized 状态、
// GetIDaaSCredentialProvider 也能成功返回 provider。此时便捷入口必须拒绝并把根因点明，
// 而不是把空 endpoint 带进下游 URL 构造后在运行期才以 "https:///v2/..." 形态暴露。
func TestGetTencentCloudCredentialsProvider_FactoryMissingEndpoint(t *testing.T) {
	factory.Reset()
	t.Cleanup(factory.Reset)

	// ClientSecretEnvVarName 指向的环境变量须有值，否则 provider 构造期可能因取不到 secret 报错，
	// 把错误路径岔到别处。这里给一个 dummy 值，确保走到 endpoint 空校验分支。
	t.Setenv("CLOUD_IDAAS_CLIENT_SECRET", "dummy-secret-for-empty-endpoint-branch")

	// 最小可过校验配置（对齐 core SDK factory.validTestConfig），故意不设 DeveloperApiEndpoint。
	cfg := &idaasconfig.IDaaSClientConfig{
		ClientId:       "test_client_id",
		InstanceId:     "test_instance_id",
		IssuerEndpoint: "https://test.example.com/oauth2",
		TokenEndpoint:  "https://test.example.com/oauth2/token",
		Scope:          "api://test.example.com|read",
		AuthnConfiguration: &idaasconfig.IdentityAuthenticationConfiguration{
			AuthnMethod:               enums.TokenAuthnMethodClientSecretPost,
			IdentityType:              enums.AuthenticationIdentityClient,
			ClientSecretEnvVarName:    "CLOUD_IDAAS_CLIENT_SECRET",
			HumanAuthenticateClientId: "iap_developer",
		},
	}
	if err := factory.InitWithConfig(cfg); err != nil {
		t.Fatalf("InitWithConfig with empty developerApiEndpoint should succeed (field is optional): %v", err)
	}

	p, err := GetTencentCloudCredentialsProvider("qcs::cam::uin/100000:roleName/demo")
	if p != nil {
		t.Fatal("expected nil provider when developerApiEndpoint is empty")
	}
	var cfgErr *sdkerrors.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *sdkerrors.ConfigError, got %T: %v", err, err)
	}
	if cfgErr.Code != adapterconstants.InvalidParameter {
		t.Fatalf("error code = %s, want %s", cfgErr.Code, adapterconstants.InvalidParameter)
	}
	if !strings.Contains(cfgErr.Message, "developerApiEndpoint") {
		t.Fatalf("error message should name the missing field, got: %s", cfgErr.Message)
	}
}

// --- 对外契约固化 ---

func TestAdapterErrorCodeConstants(t *testing.T) {
	// 错误码是对外契约的一部分（上游按码值归因与重试），固化取值避免被无意改名。
	cases := []struct{ got, want string }{
		{adapterconstants.InvalidParameter, "InvalidParameter"},
		{adapterconstants.OidcTokenError, "OidcTokenError"},
		{adapterconstants.HttpError, "HttpError"},
		{adapterconstants.PamApiError, "PamApiError"},
		{adapterconstants.ParseError, "ParseError"},
		{adapterconstants.CredentialExpired, "CredentialExpired"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("error code constant = %q, want %q", c.got, c.want)
		}
	}
}

func TestPamRequestPathTemplate(t *testing.T) {
	// Developer API 路径模板与 query 参数名是服务端契约，改动即破坏兼容。
	if adapterconstants.ObtainAccessCredentialPath != "/v2/%s/cloudAccountRoles/_/actions/obtainAccessCredential" {
		t.Fatalf("unexpected path template: %s", adapterconstants.ObtainAccessCredentialPath)
	}
	if adapterconstants.CloudAccountRoleExternalId != "cloudAccountRoleExternalId" {
		t.Fatalf("unexpected query param name: %s", adapterconstants.CloudAccountRoleExternalId)
	}
	if adapterconstants.TencentCloudStsToken != "tencentCloudStsToken" {
		t.Fatalf("unexpected response field: %s", adapterconstants.TencentCloudStsToken)
	}
}
