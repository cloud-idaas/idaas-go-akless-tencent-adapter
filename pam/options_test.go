package pam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapterconstants "github.com/cloud-idaas/idaas-go-akless-tencent-adapter/constants"
	"github.com/cloud-idaas/idaas-go-core-sdk/cache"
	sdkconstants "github.com/cloud-idaas/idaas-go-core-sdk/constants"
	sdkdomain "github.com/cloud-idaas/idaas-go-core-sdk/domain"
	"github.com/cloud-idaas/idaas-go-core-sdk/enums"
	sdkerrors "github.com/cloud-idaas/idaas-go-core-sdk/errors"
	sdkhttp "github.com/cloud-idaas/idaas-go-core-sdk/http"
)

// baseOpts 返回一组合法基础选项，测试在其上叠加待验证选项。
func baseOpts() []CredentialsProviderOption {
	return []CredentialsProviderOption{
		WithOidcTokenProvider(&fakeOidcTokenProvider{token: "tok"}),
		WithDeveloperApiEndpoint("https://pam.example.com"),
		WithIdaasInstanceId("inst-123"),
		WithRoleArn("role-arn"),
	}
}

// --- 超时取值校验（与核心 SDK config 校验同口径：0=默认，非零 ∈ [2000,60000]）---

func TestNewProvider_TimeoutValidation(t *testing.T) {
	tests := []struct {
		name    string
		opt     CredentialsProviderOption
		field   string // "connect" / "read"：期望通过的用例据此断言实际生效值
		wantErr string // 非空表示期望被拒绝，值为期望出现在消息里的字段名
		wantVal int
	}{
		{"zero connect falls back to default", WithConnectTimeout(0), "connect", "", sdkconstants.DefaultConnectTimeoutMs},
		{"zero read falls back to default", WithReadTimeout(0), "read", "", sdkconstants.DefaultReadTimeoutMs},
		{"connect lower bound accepted", WithConnectTimeout(minTimeoutMs), "connect", "", minTimeoutMs},
		{"read upper bound accepted", WithReadTimeout(maxTimeoutMs), "read", "", maxTimeoutMs},
		{"below lower bound rejected", WithConnectTimeout(1000), "connect", "connectTimeout", 0},
		{"above upper bound rejected", WithReadTimeout(600000), "read", "readTimeout", 0},
		{"negative rejected", WithConnectTimeout(-1), "connect", "connectTimeout", 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			opts := append(baseOpts(), tt.opt)
			p, err := NewIDaaSPamTencentCloudCredentialsProvider(opts...)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				// 0 必须归一成默认值，否则 GetConnectTimeout/GetReadTimeout 与实际生效值不一致。
				got := p.GetConnectTimeout()
				if tt.field == "read" {
					got = p.GetReadTimeout()
				}
				if got != tt.wantVal {
					t.Fatalf("effective timeout = %d, want %d", got, tt.wantVal)
				}
				return
			}

			if p != nil || err == nil {
				t.Fatalf("expected rejection, got p=%v err=%v", p, err)
			}
			var credErr *sdkerrors.CredentialError
			if !errors.As(err, &credErr) {
				t.Fatalf("expected *CredentialError, got %T: %v", err, err)
			}
			// 错误码与核心 SDK 配置校验保持一致，便于上游按码值统一归因。
			wantCode := sdkconstants.ConnectTimeoutNotValid
			if tt.field == "read" {
				wantCode = sdkconstants.ReadTimeoutNotValid
			}
			if credErr.Code != wantCode {
				t.Fatalf("expected code %s, got %s: %v", wantCode, credErr.Code, err)
			}
			if !strings.Contains(credErr.Message, tt.wantErr) || !strings.Contains(credErr.Message, "must be in") {
				t.Fatalf("message should name the field and the range, got: %s", credErr.Message)
			}
		})
	}
}

// --- staleValueBehavior 选项 ---

func TestNewProvider_StaleValueBehaviorValidation(t *testing.T) {
	if _, err := NewIDaaSPamTencentCloudCredentialsProvider(append(baseOpts(),
		WithStaleValueBehavior(enums.StaleValueBehavior("MAYBE")))...); err == nil {
		t.Fatal("expected rejection of unknown staleValueBehavior")
	} else {
		var credErr *sdkerrors.CredentialError
		if !errors.As(err, &credErr) || credErr.Code != sdkconstants.InvalidStaleValueBehavior {
			t.Fatalf("expected InvalidStaleValueBehavior, got %T: %v", err, err)
		}
		if !strings.Contains(err.Error(), "BLOCK") || !strings.Contains(err.Error(), "ALLOW") || !strings.Contains(err.Error(), "REFRESH") {
			t.Fatalf("message should enumerate legal values, got: %s", err.Error())
		}
	}

	for _, b := range []enums.StaleValueBehavior{"", enums.StaleValueBehaviorBlock, enums.StaleValueBehaviorAllow, enums.StaleValueBehaviorRefresh} {
		if _, err := NewIDaaSPamTencentCloudCredentialsProvider(append(baseOpts(), WithStaleValueBehavior(b))...); err != nil {
			t.Fatalf("behavior %q should be accepted, got %v", b, err)
		}
	}
}

// expiredFetch 把 provider 推进到"缓存已真过期 + PAM 故障"的状态：
// 先成功取一次，再让 PAM 报错、并把可控时钟推过 ExpiresAt。
func expiredFetch(t *testing.T, opts ...CredentialsProviderOption) (*IDaaSPamTencentCloudCredentialsProvider, *fakeClock) {
	t.Helper()
	start := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	clock := withFakeClock(t, start)

	var failNow int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		if atomic.LoadInt32(&failNow) != 0 {
			return nil, errors.New("pam unavailable")
		}
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK,
			Body: pamSuccessBody(start.Add(time.Hour).UTC().Format(time.RFC3339))}, nil
	}}
	setFail := func() { atomic.StoreInt32(&failNow, 1) }

	p, err := NewIDaaSPamTencentCloudCredentialsProvider(append([]CredentialsProviderOption{
		WithOidcTokenProvider(&fakeOidcTokenProvider{token: "tok"}),
		WithDeveloperApiEndpoint("https://pam.example.com"),
		WithIdaasInstanceId("inst-123"),
		WithRoleArn("role-arn"),
		WithHttpClient(httpClient),
	}, opts...)...)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if _, err := p.GetCredentials(); err != nil {
		t.Fatalf("warm-up GetCredentials failed: %v", err)
	}
	setFail()
	clock.advance(70 * time.Minute) // 越过 ExpiresAt，降级缓冲同样耗尽
	return p, clock
}

func TestStaleValueBehavior_BlockPropagatesRefreshError(t *testing.T) {
	p, _ := expiredFetch(t, WithStaleValueBehavior(enums.StaleValueBehaviorBlock))

	cred, err := p.GetCredentials()
	if cred != nil {
		t.Fatalf("BLOCK must never hand out a value, got %+v", cred)
	}
	// BLOCK 下核心 SDK 不做旧值降级：真实刷新失败（HttpError）原样透出，
	// 而不是被适配层转成 CredentialExpired。
	assertCredentialErrorCode(t, err, adapterconstants.HttpError)
}

func TestStaleValueBehavior_AllowStillRefusesExpiredCredential(t *testing.T) {
	p, _ := expiredFetch(t, WithStaleValueBehavior(enums.StaleValueBehaviorAllow))

	cred, err := p.GetCredentials()
	if cred != nil {
		t.Fatalf("adapter invariant: must not return an expired credential even under ALLOW, got %+v", cred)
	}
	assertCredentialErrorCode(t, err, adapterconstants.CredentialExpired)
}

func TestStaleValueBehavior_DefaultRefusesExpiredCredential(t *testing.T) {
	// 未显式设置时归一化为 REFRESH：越过降级缓冲后 fail-fast。
	p, _ := expiredFetch(t)
	_, err := p.GetCredentials()
	assertCredentialErrorCode(t, err, adapterconstants.CredentialExpired)
}

// --- HTTP 客户端解析 ---

func TestWithHttpClient_InjectedClientIsUsed(t *testing.T) {
	var used int32
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		atomic.AddInt32(&used, 1)
		return okBody(), nil
	}}
	p, err := NewIDaaSPamTencentCloudCredentialsProvider(append(baseOpts(), WithHttpClient(httpClient))...)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if _, err := p.GetCredentials(); err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if atomic.LoadInt32(&used) != 1 {
		t.Fatal("injected client must be the one issuing the PAM call")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close with injected client must not fail: %v", err)
	}
}

// 不依赖本地监听：覆盖"未注入客户端时自建核心 SDK 默认客户端"这条分支，
// 该分支此前只被两条网络端到端用例覆盖，在无监听权限的环境里会漏测。
func TestNewHTTPClient_DefaultsToCoreSdkDefaultClient(t *testing.T) {
	p, err := NewIDaaSPamTencentCloudCredentialsProvider(baseOpts()...)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	dhc, ok := p.httpClient.(*sdkhttp.DefaultHttpClient)
	if !ok {
		t.Fatalf("default client type = %T, want *sdkhttp.DefaultHttpClient", p.httpClient)
	}
	if dhc == nil {
		t.Fatal("default client must not be nil")
	}
	// Close() 依赖 http.ResourceCloser 断言回收空闲连接；默认客户端必须满足该可选接口，
	// 否则 Close() 静默 no-op 而注释声称会释放连接。
	if _, ok := interface{}(dhc).(sdkhttp.ResourceCloser); !ok {
		t.Fatal("*DefaultHttpClient must implement sdkhttp.ResourceCloser, otherwise Close() leaks idle connections")
	}
}

// --- trace 透传（beta.1 新能力经 WithHttpClient 注入的推荐姿势）---

type recordingPropagator struct{ injected int }

func (r *recordingPropagator) Inject(_ context.Context, carrier sdkhttp.HeaderCarrier) {
	r.injected++
	carrier.Set("traceparent", "00-0af7651916cd43dd8448eb2118080672-b7ad6b7169202326-01")
}

// 端到端验证 traceparent 真的落到 PAM 请求头上；无监听权限环境跳过。
func TestWithHttpClient_PropagatorInjectsTraceHeaders(t *testing.T) {
	var gotTraceparent string
	srv := startPamServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pamSuccessBody(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)))
	}))

	prop := &recordingPropagator{}
	client := sdkhttp.NewDefaultHttpClient(defaultConnectTimeout, defaultReadTimeout).WithTracePropagator(prop)

	p, err := NewIDaaSPamTencentCloudCredentialsProvider(append(baseOpts(),
		WithDeveloperApiEndpoint(srv.URL), WithHttpClient(client))...)
	if err != nil {
		t.Fatalf("constructor error: %v", err)
	}
	if _, err := p.GetCredentials(); err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if prop.injected == 0 {
		t.Fatal("propagator was never invoked — PAM call would break the trace chain")
	}
	if !strings.HasPrefix(gotTraceparent, "00-") {
		t.Fatalf("traceparent did not reach PAM request: %q", gotTraceparent)
	}
}

// --- 缓存实例不被外部污染（防御性拷贝的回归护栏）---

func TestGetCredentials_ReturnsCopyNotCacheInstance(t *testing.T) {
	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return okBody(), nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	first, err := p.GetCredentials()
	if err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	originalId := first.GetSecretId()
	first.SecretId = "MUTATED-BY-CALLER"

	second, err := p.GetCredentials()
	if err != nil {
		t.Fatalf("second GetCredentials failed: %v", err)
	}
	if second.GetSecretId() != originalId {
		t.Fatalf("cache polluted by caller mutation: got %q, want %q", second.GetSecretId(), originalId)
	}
	if first == second {
		t.Fatal("GetCredentials must return a copy, not the cached pointer")
	}
}

// 凭证响应缺 expiration 字段时也必须归因清楚（历史上会走空串 → ParseError）。
func TestGetCredentials_MissingExpirationField(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		adapterconstants.CloudAccountRoleAccessCredential: map[string]interface{}{
			adapterconstants.TencentCloudStsToken: map[string]interface{}{
				adapterconstants.TencentCloudSecretId:  "id",
				adapterconstants.TencentCloudSecretKey: "key",
				adapterconstants.TencentCloudToken:     "token",
			},
		},
	})
	p := newProviderWithFakes(t, &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK, Body: body}, nil
	}}, &fakeOidcTokenProvider{token: "tok"})

	_, err := p.GetCredentials()
	assertCredentialErrorCode(t, err, adapterconstants.ParseError)
	if !strings.Contains(err.Error(), "expiration") {
		t.Fatalf("error should point at the missing expiration field, got %v", err)
	}
}

// 三寿命点必须与 cache.GetClock() 同源：注入可控时钟后 ExpiresAt 落在预期窗口，
// 否则说明适配层与核心 SDK 用了两个时钟源（测试/宿主重配时钟时口径分叉）。
func TestRefreshResult_UsesInjectedClock(t *testing.T) {
	start := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	withFakeClock(t, start)

	httpClient := &fakeHttpClient{handler: func(context.Context, *sdkdomain.HttpRequest) (*sdkdomain.HttpResponse, error) {
		return &sdkdomain.HttpResponse{StatusCode: http.StatusOK,
			Body: pamSuccessBody(start.Add(30 * time.Minute).UTC().Format(time.RFC3339))}, nil
	}}
	p := newProviderWithFakes(t, httpClient, &fakeOidcTokenProvider{token: "tok"})

	res, err := p.refreshCredential(context.Background())
	if err != nil {
		t.Fatalf("refreshCredential failed: %v", err)
	}
	if got := res.ExpiresAt.Sub(start); got < 29*time.Minute || got > 30*time.Minute {
		t.Fatalf("ExpiresAt - receivedAt = %s, want ~30m (clock source mismatch?)", got)
	}
	if cache.GetClock().Now().Equal(res.ExpiresAt) || res.StaleTime.Before(cache.GetClock().Now()) {
		t.Fatal("lifetimes not derived from the injected clock")
	}
}
