package pam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	adapterconstants "github.com/cloud-idaas/idaas-go-akless-tencent-adapter/constants"
	"github.com/cloud-idaas/idaas-go-core-sdk/cache"
	sdkdomain "github.com/cloud-idaas/idaas-go-core-sdk/domain"
	sdkerrors "github.com/cloud-idaas/idaas-go-core-sdk/errors"
	sdkhttp "github.com/cloud-idaas/idaas-go-core-sdk/http"
	"github.com/cloud-idaas/idaas-go-core-sdk/provider"
)

// --- fakes ---

// fakeOidcTokenProvider 实现 provider.OidcTokenProvider，记录最近一次收到的 ctx。
type fakeOidcTokenProvider struct {
	token   string
	err     error
	mu      sync.Mutex
	lastCtx context.Context
	calls   int
}

func (f *fakeOidcTokenProvider) GetOidcToken() (string, error) {
	return f.GetOidcTokenWithContext(context.Background())
}

// GetOidcTokenWithContext 实现 provider.OidcTokenProvider（core SDK v0.1.0-beta.1 新增方法）。
func (f *fakeOidcTokenProvider) GetOidcTokenWithContext(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.lastCtx = ctx
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

var _ provider.OidcTokenProvider = (*fakeOidcTokenProvider)(nil)

// fakeHttpClient 记录调用次数、捕获最近一次请求与 ctx，并按 handler 返回响应。
type fakeHttpClient struct {
	handler func(ctx context.Context, req *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error)

	mu      sync.Mutex
	calls   int
	lastReq *sdkdomain.HttpRequest
	lastCtx context.Context

	// release 非空时 ExecuteContext 阻塞直到该 channel 被关闭，用于模拟"PAM 刷新很慢"，
	// 从而验证调用方 ctx 取消/超时时能立即结束等待。
	release chan struct{}
}

func (f *fakeHttpClient) Execute(req *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
	return f.ExecuteContext(context.Background(), req)
}

// ExecuteContext 实现 sdkhttp.HttpClient（core SDK v0.1.0-beta.1 新增方法）。
func (f *fakeHttpClient) ExecuteContext(ctx context.Context, req *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
	f.mu.Lock()
	f.calls++
	f.lastReq = req
	f.lastCtx = ctx
	f.mu.Unlock()

	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.handler == nil {
		return nil, errors.New("fakeHttpClient: no handler")
	}
	return f.handler(ctx, req)
}

// Get / Post 是 sdkhttp.HttpClient 接口的遗留方法，本适配器刷新链路只走 ExecuteContext。
// 早期桩实现转调 f.Execute(nil)，会把 nil HttpRequest 透传给 handler，触发空指针 panic
// 且栈帧指向 fake 而非真实调用点，排障困难。改为返回明确错误：接口契约仍满足（编译期
// 断言 var _ sdkhttp.HttpClient 不变），任何误用立即在调用方暴露真实原因。
func (f *fakeHttpClient) Get(_ string, _ map[string]string) (*sdkdomain.HttpResponse, error) {
	return nil, errors.New("fakeHttpClient.Get is not implemented; refresh path uses ExecuteContext")
}

func (f *fakeHttpClient) Post(_ string, _ map[string]string, _ interface{}) (*sdkdomain.HttpResponse, error) {
	return nil, errors.New("fakeHttpClient.Post is not implemented; refresh path uses ExecuteContext")
}

// callCount 返回已发起的 PAM 调用次数（加锁读，刷新可能来自后台 goroutine）。
func (f *fakeHttpClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var _ sdkhttp.HttpClient = (*fakeHttpClient)(nil)

// fakeClock 可控时钟，用于验证核心 SDK 双时间点模型与降级/过期 fail-fast 分支。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// withFakeClock 安装可控时钟并在测试结束后恢复原时钟。
func withFakeClock(t *testing.T, start time.Time) *fakeClock {
	t.Helper()
	c := &fakeClock{now: start}
	prev := cache.GetClock()
	cache.SetClock(c)
	t.Cleanup(func() { cache.SetClock(prev) })
	return c
}

// --- helpers ---
// startPamServerOrSkip 启动本地 PAM mock HTTP 服务。
//
// 受限环境（无监听权限的沙箱/CI）下 net.Listen 返回 EPERM，此时跳过而不是失败；
// 其余逻辑由注入的 fakeHttpClient 覆盖，故跳过不影响分支覆盖。
func startPamServerOrSkip(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable, skipping end-to-end HTTP test: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}} //nolint:staticcheck // 显式 IPv4 回环
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func pamSuccessBody(expiration string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		adapterconstants.CloudAccountRoleAccessCredential: map[string]interface{}{
			adapterconstants.TencentCloudStsToken: map[string]interface{}{
				adapterconstants.TencentCloudSecretId:   "AKID********",
				adapterconstants.TencentCloudSecretKey:  "secret-key-value",
				adapterconstants.TencentCloudToken:      "session-token-value",
				adapterconstants.TencentCloudExpiration: expiration,
			},
		},
	})
	return b
}

func okBody() *sdkdomain.HttpResponse {
	return &sdkdomain.HttpResponse{
		StatusCode: http.StatusOK,
		Body:       pamSuccessBody(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
	}
}

// newProviderWithFakes 构造注入了 fake http + fake oidc 的 provider。
func newProviderWithFakes(t *testing.T, httpClient *fakeHttpClient, oidc provider.OidcTokenProvider) *IDaaSPamTencentCloudCredentialsProvider {
	t.Helper()
	p, err := NewIDaaSPamTencentCloudCredentialsProvider(
		WithOidcTokenProvider(oidc),
		WithDeveloperApiEndpoint("https://pam.example.com"),
		WithIdaasInstanceId("inst-123"),
		WithRoleArn("qcs::cam::uin/100000:roleName/demo-role"),
		WithHttpClient(httpClient),
	)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	return p
}

// assertCredentialErrorCode 断言 err 是携带期望码值的 CredentialError。
func assertCredentialErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", wantCode)
	}
	var credErr *sdkerrors.CredentialError
	if !errors.As(err, &credErr) {
		t.Fatalf("expected *sdkerrors.CredentialError, got %T: %v", err, err)
	}
	if credErr.Code != wantCode {
		t.Fatalf("error code = %s, want %s (message: %s)", credErr.Code, wantCode, credErr.Message)
	}
}

// --- 构造器 ---

func TestNewIDaaSPamTencentCloudCredentialsProvider_Defaults(t *testing.T) {
	oidc := &fakeOidcTokenProvider{token: "tok"}
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) { return okBody(), nil }}
	p := newProviderWithFakes(t, httpClient, oidc)

	if p.GetRoleArn() == "" || p.GetIdaasInstanceId() != "inst-123" {
		t.Fatalf("unexpected accessors: roleArn=%q instanceId=%q", p.GetRoleArn(), p.GetIdaasInstanceId())
	}
	if p.GetDeveloperApiEndpoint() != "https://pam.example.com" {
		t.Fatalf("developerApiEndpoint = %q", p.GetDeveloperApiEndpoint())
	}
	if p.GetConnectTimeout() != defaultConnectTimeout || p.GetReadTimeout() != defaultReadTimeout {
		t.Fatalf("timeouts = (%d,%d), want (%d,%d)", p.GetConnectTimeout(), p.GetReadTimeout(),
			defaultConnectTimeout, defaultReadTimeout)
	}
	if p.GetOIDCToken() != "" {
		t.Fatalf("GetOIDCToken should be empty before first refresh, got %q", p.GetOIDCToken())
	}
	if p.supplier == nil {
		t.Fatal("supplier not initialised — core SDK CachedResultSupplier wiring missing")
	}
}

func TestNewIDaaSPamTencentCloudCredentialsProvider_MissingRequiredParams(t *testing.T) {
	good := []CredentialsProviderOption{
		WithOidcTokenProvider(&fakeOidcTokenProvider{token: "tok"}),
		WithDeveloperApiEndpoint("https://pam.example.com"),
		WithIdaasInstanceId("inst-123"),
		WithRoleArn("role-arn"),
	}

	tests := []struct {
		name    string
		dropIdx int
		wantMsg string
	}{
		{"missing roleArn", 3, "roleArn cannot be empty"},
		{"missing oidcTokenProvider", 0, "oidcTokenProvider cannot be nil"},
		{"missing idaasInstanceId", 2, "idaasInstanceId cannot be empty"},
		{"missing developerApiEndpoint", 1, "developerApiEndpoint cannot be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []CredentialsProviderOption
			for i, opt := range good {
				if i == tt.dropIdx {
					continue
				}
				opts = append(opts, opt)
			}
			p, err := NewIDaaSPamTencentCloudCredentialsProvider(opts...)
			if p != nil {
				t.Fatal("expected nil provider on invalid config")
			}
			var credErr *sdkerrors.CredentialError
			if !errors.As(err, &credErr) || credErr.Code != adapterconstants.InvalidParameter {
				t.Fatalf("expected InvalidParameter, got %v", err)
			}
			if !strings.Contains(credErr.Message, tt.wantMsg) {
				t.Fatalf("message = %q, want contains %q", credErr.Message, tt.wantMsg)
			}
		})
	}
}

// --- 正常换取凭证 ---

func TestGetCredentials_Success(t *testing.T) {
	expiration := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK, Body: pamSuccessBody(expiration)}, nil
	}}
	oidc := &fakeOidcTokenProvider{token: "oidc-token"}
	p := newProviderWithFakes(t, httpClient, oidc)

	cred, err := p.GetCredentials()
	if err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if cred.GetSecretId() != "AKID********" || cred.GetSecretKey() != "secret-key-value" || cred.GetToken() != "session-token-value" {
		t.Fatalf("unexpected credential: %+v", cred)
	}
	if got := cred.GetExpiration().UTC().Format(time.RFC3339); got != expiration {
		t.Fatalf("expiration = %s, want %s", got, expiration)
	}

	// 请求构造：URL / query / headers 均由核心 SDK 的 HttpRequest 承载。
	httpClient.mu.Lock()
	req := httpClient.lastReq
	httpClient.mu.Unlock()
	if req == nil {
		t.Fatal("no HTTP request captured")
	}
	if want := "https://pam.example.com/v2/inst-123/cloudAccountRoles/_/actions/obtainAccessCredential"; req.URL != want {
		t.Fatalf("URL = %q, want %q", req.URL, want)
	}
	if got := req.QueryParams.Get(adapterconstants.CloudAccountRoleExternalId); got != p.GetRoleArn() {
		t.Fatalf("query %s = %q, want %q", adapterconstants.CloudAccountRoleExternalId, got, p.GetRoleArn())
	}
	if got := req.Headers["Authorization"]; got != "Bearer oidc-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer oidc-token")
	}
	if got := req.Headers["Accept"]; got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if p.GetOIDCToken() != "oidc-token" {
		t.Fatalf("GetOIDCToken = %q, want oidc-token", p.GetOIDCToken())
	}
}

func TestGetCredentials_CachesResultUntilStale(t *testing.T) {
	var calls int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		atomic.AddInt32(&calls, 1)
		return okBody(), nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	for i := 0; i < 5; i++ {
		if _, err := p.GetCredentials(); err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("PAM called %d times, want 1 (credential should be cached)", got)
	}
}

func TestGetCredentials_EscapesInstanceIdInPath(t *testing.T) {
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return okBody(), nil
	}}
	p, err := NewIDaaSPamTencentCloudCredentialsProvider(
		WithOidcTokenProvider(&fakeOidcTokenProvider{token: "tok"}),
		WithDeveloperApiEndpoint("https://pam.example.com"),
		WithIdaasInstanceId("inst/../evil"),
		WithRoleArn("role-arn"),
		WithHttpClient(httpClient),
	)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if _, err := p.GetCredentials(); err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	httpClient.mu.Lock()
	req := httpClient.lastReq
	httpClient.mu.Unlock()
	if strings.Contains(req.URL, "/v2/inst/../evil/") {
		t.Fatalf("instanceId was not path-escaped: %s", req.URL)
	}
	if !strings.Contains(req.URL, "%2F") {
		t.Fatalf("expected escaped instanceId in URL, got %s", req.URL)
	}
}

// --- 异常分支 ---

func TestGetCredentials_ErrorPaths(t *testing.T) {
	pamResp := func(status int, body string) *sdkdomain.HttpResponse {
		return &sdkdomain.HttpResponse{StatusCode: status, Body: []byte(body)}
	}

	tests := []struct {
		name     string
		httpFunc func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error)
		oidc     provider.OidcTokenProvider
		wantCode string
	}{
		{
			name:     "oidc provider error",
			oidc:     &fakeOidcTokenProvider{err: errors.New("token endpoint down")},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) { return okBody(), nil },
			wantCode: adapterconstants.OidcTokenError,
		},
		{
			name:     "empty oidc token",
			oidc:     &fakeOidcTokenProvider{token: ""},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) { return okBody(), nil },
			wantCode: adapterconstants.OidcTokenError,
		},
		{
			name: "transport error",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return nil, errors.New("dial tcp: connection refused")
			},
			wantCode: adapterconstants.HttpError,
		},
		{
			name: "pam 403",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusForbidden, `{"error":"denied"}`), nil
			},
			wantCode: adapterconstants.PamApiError,
		},
		{
			name: "invalid json",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, "not-json"), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "missing access credential",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "access credential wrong type",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":"str"}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "missing sts token object",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":{}}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "sts token wrong type",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":{"tencentCloudStsToken":123}}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "empty sts fields",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":{"tencentCloudStsToken":{"tmpSecretId":"","tmpSecretKey":"","token":"","expiration":"2099-01-01T00:00:00Z"}}}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "unparsable expiration",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":{"tencentCloudStsToken":{"tmpSecretId":"id","tmpSecretKey":"key","token":"tok","expiration":"tomorrow"}}}`), nil
			},
			wantCode: adapterconstants.ParseError,
		},
		{
			name: "already expired expiration",
			oidc: &fakeOidcTokenProvider{token: "tok"},
			httpFunc: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
				return pamResp(http.StatusOK, `{"cloudAccountRoleAccessCredential":{"tencentCloudStsToken":{"tmpSecretId":"id","tmpSecretKey":"key","token":"tok","expiration":"2001-01-01T00:00:00Z"}}}`), nil
			},
			wantCode: adapterconstants.CredentialExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProviderWithFakes(t, &fakeHttpClient{handler: tt.httpFunc}, tt.oidc)
			cred, err := p.GetCredentials()
			if cred != nil {
				t.Fatalf("expected nil credential, got %+v", cred)
			}
			assertCredentialErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestGetCredentials_EmFieldsErrorDoesNotLeakSecrets(t *testing.T) {
	// 2xx 响应体里有真实 SecretKey；缺 SecretId 时错误消息只能点名缺失字段，不能回显 body。
	body := `{"cloudAccountRoleAccessCredential":{"tencentCloudStsToken":` +
		`{"tmpSecretId":"","tmpSecretKey":"SUPER-SECRET","token":"SESSION-TOKEN","expiration":"2099-01-01T00:00:00Z"}}}`
	p := newProviderWithFakes(t, &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK, Body: []byte(body)}, nil
	}}, &fakeOidcTokenProvider{token: "tok"})

	_, err := p.GetCredentials()
	assertCredentialErrorCode(t, err, adapterconstants.ParseError)
	msg := err.Error()
	if strings.Contains(msg, "SUPER-SECRET") || strings.Contains(msg, "SESSION-TOKEN") {
		t.Fatalf("error message leaked credentials: %s", msg)
	}
	if !strings.Contains(msg, adapterconstants.TencentCloudSecretId) {
		t.Fatalf("error message should name the missing field, got: %s", msg)
	}
}

// --- context 生命周期 ---

func TestGetCredentialsWithContext_CancelledCallerReturnsPromptly(t *testing.T) {
	release := make(chan struct{})
	httpClient := &fakeHttpClient{
		release: release,
		handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) { return okBody(), nil },
	}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 调用方已取消 → 不应干等在飞刷新

	start := time.Now()
	cred, err := p.GetCredentialsWithContext(ctx)
	elapsed := time.Since(start)

	if cred != nil || err == nil {
		t.Fatalf("expected error on cancelled ctx, got cred=%v err=%v", cred, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancelled ctx waited %s for refresh; should return immediately", elapsed)
	}
	var cacheErr *sdkerrors.CacheError
	if !errors.As(err, &cacheErr) || cacheErr.Code != "ClientRequestCanceled" {
		t.Fatalf("expected ClientRequestCanceled, got %T: %v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cause chain should preserve context.Canceled, got %v", err)
	}
}

func TestGetCredentialsWithContext_NilContextFallsBackToBackground(t *testing.T) {
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return okBody(), nil
	}}
	oidc := &fakeOidcTokenProvider{token: "tok"}
	p := newProviderWithFakes(t, httpClient, oidc)

	cred, err := p.GetCredentialsWithContext(nil) //nolint:staticcheck // SA1012: 故意传 nil，验证兜底
	if err != nil {
		t.Fatalf("nil ctx should be tolerated, got %v", err)
	}
	if cred == nil {
		t.Fatal("expected credential")
	}

	// refreshFunc 收到的是核心 SDK 派生的非 nil ctx（刷新链路与请求解耦）。
	oidc.mu.Lock()
	oidcCtx := oidc.lastCtx
	oidc.mu.Unlock()
	if oidcCtx == nil {
		t.Fatal("refreshCredential must not pass nil ctx to OidcTokenProvider")
	}
	httpClient.mu.Lock()
	reqCtx := httpClient.lastCtx
	httpClient.mu.Unlock()
	if reqCtx == nil {
		t.Fatal("refreshCredential must use ExecuteContext with non-nil ctx")
	}
}

// --- 核心 SDK 双时间点模型 / 降级与过期 fail-fast ---

func TestRefreshCredential_BuildsLifetimesViaCoreSDK(t *testing.T) {
	start := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	clock := withFakeClock(t, start)

	const ttl = time.Hour
	expiration := start.Add(ttl).UTC().Format(time.RFC3339)
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK, Body: pamSuccessBody(expiration)}, nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	result, err := p.refreshCredential(context.Background())
	if err != nil {
		t.Fatalf("refreshCredential failed: %v", err)
	}

	expiresAt := start.Add(ttl)
	// ExpiresAt = PAM 返回的真实过期时间（expiresIn 秒级向下取整，最多提前 1s，偏安全方向）。
	if result.ExpiresAt.Before(expiresAt.Add(-time.Second)) || result.ExpiresAt.After(expiresAt) {
		t.Fatalf("ExpiresAt = %s, want ~%s", result.ExpiresAt, expiresAt)
	}

	basePrefetch := expiresAt.Add(-ttl / 3) // 2/3 寿命点：后台预取
	baseStale := expiresAt.Add(-ttl / 5)    // 4/5 寿命点：同步刷新
	// 核心 SDK 不变量：PrefetchTime <= StaleTime < ExpiresAt；jitter 只做区间收缩，
	// 因此不会早于各自基准点，也不会越过上界。
	if result.PrefetchTime.Before(basePrefetch) || result.PrefetchTime.After(result.StaleTime) {
		t.Fatalf("PrefetchTime = %s, want in [%s, %s]", result.PrefetchTime, basePrefetch, result.StaleTime)
	}
	if result.StaleTime.Before(baseStale) || !result.StaleTime.Before(result.ExpiresAt) {
		t.Fatalf("StaleTime = %s, want in [%s, %s)", result.StaleTime, baseStale, result.ExpiresAt)
	}

	// 刚取回时（start）三个点都未到：不预取、不 stale、未过期 → 后续请求直接命中缓存。
	if result.ShouldPrefetch() || result.IsStale() || result.IsExpired() {
		t.Fatalf("at fetch time: prefetch=%v stale=%v expired=%v, want all false",
			result.ShouldPrefetch(), result.IsStale(), result.IsExpired())
	}

	clock.advance(result.PrefetchTime.Sub(start) + time.Millisecond)
	if !result.ShouldPrefetch() {
		t.Fatal("past PrefetchTime, ShouldPrefetch must be true")
	}
	if result.IsStale() {
		t.Fatal("before StaleTime, IsStale must be false")
	}

	clock.advance(result.StaleTime.Sub(clock.Now()) + time.Millisecond)
	if !result.IsStale() || result.IsExpired() {
		t.Fatalf("in stale window: stale=%v expired=%v, want true/false", result.IsStale(), result.IsExpired())
	}

	clock.advance(result.ExpiresAt.Sub(clock.Now()) + time.Millisecond)
	if !result.IsExpired() {
		t.Fatal("past ExpiresAt, IsExpired must be true")
	}
}

func TestGetCredentials_FailFastWhenCachedCredentialExpired(t *testing.T) {
	start := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	clock := withFakeClock(t, start)

	// failNow 用原子量：refreshFunc 由核心 SDK 的后台 goroutine 执行，读侧与测试写侧并发。
	var failNow int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		if atomic.LoadInt32(&failNow) != 0 {
			return nil, errors.New("pam unavailable")
		}
		return &sdkdomain.HttpResponse{
			StatusCode: http.StatusOK,
			Body:       pamSuccessBody(start.Add(time.Hour).UTC().Format(time.RFC3339)),
		}, nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	cred, err := p.GetCredentials()
	if err != nil || cred == nil {
		t.Fatalf("first GetCredentials failed: %v", err)
	}

	// PAM 故障 + 缓存凭证已真正过期（降级缓冲耗尽）：绝不把过期凭证交给下游。
	atomic.StoreInt32(&failNow, 1)
	clock.advance(70 * time.Minute)

	cred, err = p.GetCredentialsWithContext(context.Background())
	if cred != nil {
		t.Fatalf("must not return an expired credential, got %+v", cred)
	}
	assertCredentialErrorCode(t, err, adapterconstants.CredentialExpired)

	// 退避窗口内同样 fail-fast，且不再逐请求打 PAM。
	before := httpClient.callCount()
	if _, err = p.GetCredentials(); err == nil {
		t.Fatal("expected CredentialExpired on second attempt too")
	}
	assertCredentialErrorCode(t, err, adapterconstants.CredentialExpired)
	if got := httpClient.callCount() - before; got > 1 {
		t.Fatalf("PAM call amplification after expiry: +%d calls", got)
	}
}

// --- 默认 HTTP 客户端端到端（真实 httptest 服务）---

func TestGetCredentials_EndToEndWithDefaultHttpClient(t *testing.T) {
	var gotAuth, gotAccept, gotQuery, gotPath string
	srv := startPamServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get(adapterconstants.CloudAccountRoleExternalId)
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pamSuccessBody(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)))
	}))

	oidc := &fakeOidcTokenProvider{token: "e2e-oidc-token"}
	p, err := NewIDaaSPamTencentCloudCredentialsProvider(
		WithOidcTokenProvider(oidc),
		WithDeveloperApiEndpoint(srv.URL),
		WithIdaasInstanceId("inst-e2e"),
		WithRoleArn("role-arn-e2e"),
	)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}

	cred, err := p.GetCredentialsWithContext(context.Background())
	if err != nil {
		t.Fatalf("GetCredentialsWithContext failed: %v", err)
	}
	if cred.GetSecretId() != "AKID********" {
		t.Fatalf("unexpected credential: %+v", cred)
	}
	if want := fmt.Sprintf("/v2/%s/cloudAccountRoles/_/actions/obtainAccessCredential", "inst-e2e"); gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotQuery != "role-arn-e2e" {
		t.Fatalf("roleArn query = %q", gotQuery)
	}
	if gotAuth != "Bearer e2e-oidc-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}

func TestGetCredentials_Non2xxEndToEnd(t *testing.T) {
	srv := startPamServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error_code":"InternalError","message":"boom"}`))
	}))

	p, err := NewIDaaSPamTencentCloudCredentialsProvider(
		WithOidcTokenProvider(&fakeOidcTokenProvider{token: "tok"}),
		WithDeveloperApiEndpoint(srv.URL),
		WithIdaasInstanceId("inst-1"),
		WithRoleArn("role"),
	)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}

	_, err = p.GetCredentials()
	assertCredentialErrorCode(t, err, adapterconstants.PamApiError)
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should carry status code, got %v", err)
	}
}

// --- 并发 ---

func TestGetCredentials_ConcurrentAccessIsSafe(t *testing.T) {
	var calls int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		atomic.AddInt32(&calls, 1)
		return okBody(), nil
	}}
	oidc := &fakeOidcTokenProvider{token: "tok"}
	p := newProviderWithFakes(t, httpClient, oidc)

	// 先串行取一次，把缓存预热到"新鲜且未到 stale 窗口"的稳定态——这才是真实运行时的常态
	// （凭证寿命 1h，2/3 寿命点之前都命中缓存）。
	if _, err := p.GetCredentials(); err != nil {
		t.Fatalf("warm-up GetCredentials failed: %v", err)
	}
	warmCalls := atomic.LoadInt32(&calls)

	const goroutines = 16
	var wg sync.WaitGroup
	results := make(chan string, goroutines)
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred, err := p.GetCredentials()
			if err != nil {
				errCh <- err
				return
			}
			if cred.GetSecretId() == "" || cred.GetSecretKey() == "" || cred.GetToken() == "" {
				errCh <- errors.New("incomplete credential")
				return
			}
			// 调用方改写返回值不得影响缓存或其他 goroutine。
			cred.SecretId = "MUTATED"
			results <- cred.GetToken()
			_ = p.GetOIDCToken()
		}()
	}
	wg.Wait()
	close(errCh)
	close(results)

	for err := range errCh {
		t.Fatalf("concurrent GetCredentials failed: %v", err)
	}
	// 断言一：稳态并发不得再打 PAM——命中缓存的并发读必须是零外部调用。
	if got := atomic.LoadInt32(&calls) - warmCalls; got != 0 {
		t.Fatalf("warm cache issued %d extra PAM calls under 16 concurrent readers, want 0", got)
	}
	// 断言二：所有 goroutine 拿到的是同一份会话凭证（缓存一致性 + 拷贝隔离）。
	seen := map[string]bool{}
	for tok := range results {
		seen[tok] = true
	}
	if len(seen) != 1 || !seen["session-token-value"] {
		t.Fatalf("concurrent readers saw differing credentials: %v", seen)
	}
}

// 冷启动并发首取：**不**断言 PAM 调用次数。核心 SDK 的 supplier 先在锁外读缓存、再在锁内
// 登记刷新 leader，两步不原子；刷新耗时极短时在飞窗口几乎不存在，等待合并路径（waitInflight）
// 基本不生效，因此调用次数可以远高于 1（本机实测 -count=30 下最高 7 次，理论上界是并发度）。
// 这里只钉住真正的不变量：每个调用方都拿到可用且有效的凭证、不出现半提交/空值/panic。
// 冷启动窗口的存在与各语言适配层的口径已在 README「配置生效范围」写明。
func TestGetCredentials_ColdStartBurstAllCallersSucceed(t *testing.T) {
	var calls int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		atomic.AddInt32(&calls, 1)
		return okBody(), nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	const goroutines = 16
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred, err := p.GetCredentialsWithContext(context.Background())
			if err != nil {
				errCh <- err
				return
			}
			if cred.GetExpiration().Before(time.Now()) {
				errCh <- fmt.Errorf("handed out an already-expired credential: %+v", cred)
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("cold-start burst failed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got < 1 {
		t.Fatalf("cold start issued %d PAM calls, want >= 1", got)
	}
}

// --- 纯函数 ---

func TestParseUTCDate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"rfc3339 z", "2026-10-27T10:00:00Z", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)},
		{"rfc3339 offset", "2026-10-27T18:00:00+08:00", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)},
		{"fractional seconds", "2026-10-27T10:00:00.123Z", time.Date(2026, 10, 27, 10, 0, 0, 123000000, time.UTC)},
		{"space separated", "2026-10-27 10:00:00", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUTCDate(tt.input)
			if err != nil {
				t.Fatalf("parseUTCDate(%q) error: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseUTCDate(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}

	for _, bad := range []string{"", "tomorrow", "2026-13-45T99:99:99Z"} {
		if _, err := parseUTCDate(bad); err == nil {
			t.Fatalf("parseUTCDate(%q) expected error, got nil", bad)
		}
	}
}

func TestTruncateBody(t *testing.T) {
	small := strings.Repeat("a", 10)
	if got := truncateBody([]byte(small)); got != small {
		t.Fatalf("small body should pass through, got %q", got)
	}
	huge := strings.Repeat("b", maxErrorBodyLen+100)
	got := truncateBody([]byte(huge))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("truncated body must carry suffix, got len=%d", len(got))
	}
	if len(got) != maxErrorBodyLen+len("...(truncated)") {
		t.Fatalf("unexpected truncated length %d", len(got))
	}

	// 多字节 UTF-8：截断点必须落在 rune 边界，不能从字符中间切开产出无效 UTF-8。
	// "中" 是 3 字节 rune；构造一个刚好让 maxErrorBodyLen 切在某个 "中" 中间的 body。
	utf8Body := strings.Repeat("中", maxErrorBodyLen) // 3 * maxErrorBodyLen 字节
	got = truncateBody([]byte(utf8Body))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("utf8 truncated body must carry suffix, got len=%d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated body must be valid UTF-8, got %q", got)
	}
	// 回退到 rune 边界后，前缀长度应严格小于 maxErrorBodyLen（被切的多字节字符整体被让出）。
	prefix := strings.TrimSuffix(got, "...(truncated)")
	if rc := utf8.RuneCountInString(prefix); rc <= 0 || utf8.RuneCountInString(prefix)*3 != len(prefix) {
		t.Fatalf("truncated prefix not rune-aligned: runes=%d bytes=%d", rc, len(prefix))
	}
}
