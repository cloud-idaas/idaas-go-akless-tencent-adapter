package pam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	adapterconstants "github.com/cloud-idaas/idaas-go-akless-tencent-adapter/constants"
	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/domain"
	"github.com/cloud-idaas/idaas-go-core-sdk/cache"
	sdkconstants "github.com/cloud-idaas/idaas-go-core-sdk/constants"
	sdkdomain "github.com/cloud-idaas/idaas-go-core-sdk/domain"
	"github.com/cloud-idaas/idaas-go-core-sdk/enums"
	sdkerrors "github.com/cloud-idaas/idaas-go-core-sdk/errors"
	sdkhttp "github.com/cloud-idaas/idaas-go-core-sdk/http"
	"github.com/cloud-idaas/idaas-go-core-sdk/provider"
)

// 超时默认值与合法区间均取自核心 SDK 的单一来源（constants.Default*TimeoutMs 与
// config 校验的 [2000,60000]ms），避免适配器另立一套口径后与核心 SDK 分叉：
// 下界 2s 防 DNS 抖动误超时，上界 60s 防 PAM 局部故障放大成整进程阻塞。
const (
	defaultConnectTimeout = sdkconstants.DefaultConnectTimeoutMs
	defaultReadTimeout    = sdkconstants.DefaultReadTimeoutMs
	minTimeoutMs          = 2000
	maxTimeoutMs          = 60000

	// maxErrorBodyLen 错误消息中引用的响应体最大长度，避免超大 body 膨胀日志。
	maxErrorBodyLen = 512
)

// IDaaSPamTencentCloudCredentialsProvider 通过 IDaaS PAM Developer API 用 OIDC Token
// 换取腾讯云 STS 临时凭证（SecretId / SecretKey / Token），供腾讯云 SDK 使用。
//
// 缓存与刷新复用核心 SDK 的 cache.CachedResultSupplier（StaleValueBehaviorRefresh +
// singleflight），寿命点由核心 SDK 的 cache.NewRefreshResult 统一计算（预取 2/3 寿命、
// stale 4/5 寿命，含防惊群 jitter）。刷新失败时降级返回仍在缓冲期内的旧凭证；旧凭证
// 亦真正过期时 fail-fast，绝不把过期凭证交给下游。
//
// 并发安全：凭证获取与后台预取可并发执行，可在多个腾讯云客户端实例间共享同一 provider。
type IDaaSPamTencentCloudCredentialsProvider struct {
	roleArn              string
	oidcToken            atomic.Value // 最近一次使用的 OIDC Token，供 GetOIDCToken() 观测
	oidcTokenProvider    provider.OidcTokenProvider
	connectTimeout       int
	readTimeout          int
	idaasInstanceId      string
	developerApiEndpoint string
	developerApiPath     string
	httpClient           sdkhttp.HttpClient
	supplier             *cache.CachedResultSupplier[*domain.TencentCloudCredential]
}

// CredentialsProviderOption 配置 IDaaSPamTencentCloudCredentialsProvider 的函数式选项。
type CredentialsProviderOption func(*credentialsProviderConfig)

// credentialsProviderConfig 是 functional option 的内部配置载体。
type credentialsProviderConfig struct {
	developerApiEndpoint string
	idaasInstanceId      string
	oidcTokenProvider    provider.OidcTokenProvider
	roleArn              string
	connectTimeout       int
	readTimeout          int
	httpClient           sdkhttp.HttpClient // WithHttpClient 注入（高级用法/测试打桩）
	staleValueBehavior   enums.StaleValueBehavior
	tlsSkipVerify        bool // 仅由便捷入口从核心 SDK 配置透传，不对外暴露开关
}

// WithDeveloperApiEndpoint 设置 PAM Developer API 端点（含协议前缀）。
func WithDeveloperApiEndpoint(endpoint string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.developerApiEndpoint = endpoint
	}
}

// WithIdaasInstanceId 设置 IDaaS 实例 ID。
func WithIdaasInstanceId(instanceId string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.idaasInstanceId = instanceId
	}
}

// WithOidcTokenProvider 设置 OIDC Token 提供者（通常由核心 SDK 凭证提供者经
// oidcTokenAdapter 桥接而来，测试中可直接注入桩实现）。
func WithOidcTokenProvider(p provider.OidcTokenProvider) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.oidcTokenProvider = p
	}
}

// WithRoleArn 设置云账号角色外部标识（roleArn）。
func WithRoleArn(roleArn string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.roleArn = roleArn
	}
}

// WithConnectTimeout 设置连接超时（毫秒）。0 表示用默认值；合法区间 [2000, 60000]，
// 越界在构造期以 ConnectTimeoutNotValid 拒绝（与核心 SDK 配置校验同口径，不再静默接受）。
func WithConnectTimeout(timeout int) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.connectTimeout = timeout
	}
}

// WithReadTimeout 设置读取超时（毫秒）。0 表示用默认值；合法区间 [2000, 60000]，
// 越界在构造期以 ReadTimeoutNotValid 拒绝。
func WithReadTimeout(timeout int) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.readTimeout = timeout
	}
}

// WithHttpClient 注入自定义 HTTP 客户端（高级用法：代理、mTLS、自签名证书跳过校验、
// trace 透传等）。注入后适配器不再自建客户端，超时/TLS/trace 全部由宿主客户端负责——
// 与核心 SDK factory.WithHttpClient 的规则一致（"外部注入的 client 自行负责 trace 透传"）。
//
// 想要 trace 连续性，直接把配好的默认客户端交进来即可：
//
//	sdkhttp.NewDefaultHttpClient(5000, 10000).WithTracePropagator(myPropagator)
//
// 也是唯一的外部注入点：适配器**不提供**任何关闭 TLS 证书校验的公开选项，
// 需要跳过校验必须由宿主自己构造 http.Client（责任显式落在宿主侧）。
func WithHttpClient(client sdkhttp.HttpClient) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.httpClient = client
	}
}

// WithStaleValueBehavior 设置凭证进入 stale 窗口后的取值策略：BLOCK / ALLOW / REFRESH。
// 空值归一化为 REFRESH（与核心 SDK 一致）；其余取值构造期以 InvalidStaleValueBehavior 拒绝。
//
// 注意：核心 SDK 的 staleValueBehavior 配置项只作用于其**自身** token 缓存，不会传导到
// 适配层的 PAM 凭证缓存——核心 SDK v0.1.0-beta.1 未暴露读取该字段的包级 API
// （只有已 Deprecated 的 GetInstance().GetConfig()）。需要非默认策略时显式用本选项设置。
// 详见 README「配置生效范围」。
func WithStaleValueBehavior(behavior enums.StaleValueBehavior) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.staleValueBehavior = behavior
	}
}

// withUnsafeIgnoreSSLCert 透传租户级 httpConfiguration.unsafeIgnoreSslCert。
//
// 刻意不对外暴露：该值只可能来自核心 SDK 配置，而宿主一旦 factory.WithTLSEnforcement()，
// unsafeIgnoreSslCert=true 会在 Initialize 阶段被硬拒（InsecureTLSDisallowed），
// 因此从工厂读到的值天然不会绕过 enforcement。全参构造器的用户若确有需要，
// 用 WithHttpClient 自带客户端，把"是否跳过校验"的决定权明确留在自己代码里。
func withUnsafeIgnoreSSLCert(skip bool) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.tlsSkipVerify = skip
	}
}

// NewIDaaSPamTencentCloudCredentialsProvider 按 functional options 创建凭证提供者。
//
// roleArn / oidcTokenProvider / idaasInstanceId / developerApiEndpoint 均为必填，
// 缺失即返回 InvalidParameter，避免空值进入下游 URL 构造后才在运行期暴露。
func NewIDaaSPamTencentCloudCredentialsProvider(opts ...CredentialsProviderOption) (*IDaaSPamTencentCloudCredentialsProvider, error) {
	cfg := &credentialsProviderConfig{
		connectTimeout: defaultConnectTimeout,
		readTimeout:    defaultReadTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.roleArn == "" {
		return nil, sdkerrors.NewCredentialError(adapterconstants.InvalidParameter, "roleArn cannot be empty", nil)
	}
	if cfg.oidcTokenProvider == nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.InvalidParameter, "oidcTokenProvider cannot be nil", nil)
	}
	if cfg.idaasInstanceId == "" {
		return nil, sdkerrors.NewCredentialError(adapterconstants.InvalidParameter, "idaasInstanceId cannot be empty", nil)
	}
	if cfg.developerApiEndpoint == "" {
		return nil, sdkerrors.NewCredentialError(adapterconstants.InvalidParameter, "developerApiEndpoint cannot be empty", nil)
	}
	// 超时区间校验与核心 SDK config 校验同口径：0 = 未设置（回默认值），
	// 非零值必须落在 [2000,60000]ms。核心 SDK 只兜 <=0，越界的大值此前会被静默接受，
	// 例如 readTimeout=600000 让一次 PAM 故障阻塞业务线程 10 分钟。
	if err := validateTimeout("connectTimeout", cfg.connectTimeout, sdkconstants.ConnectTimeoutNotValid); err != nil {
		return nil, err
	}
	if err := validateTimeout("readTimeout", cfg.readTimeout, sdkconstants.ReadTimeoutNotValid); err != nil {
		return nil, err
	}
	if cfg.connectTimeout == 0 {
		cfg.connectTimeout = defaultConnectTimeout
	}
	if cfg.readTimeout == 0 {
		cfg.readTimeout = defaultReadTimeout
	}
	// stale 策略：空值归一化为 REFRESH（与核心 SDK 归一化行为一致，零回归）。
	staleBehavior := cfg.staleValueBehavior
	if staleBehavior == "" {
		staleBehavior = enums.StaleValueBehaviorRefresh
	}
	switch staleBehavior {
	case enums.StaleValueBehaviorBlock, enums.StaleValueBehaviorAllow, enums.StaleValueBehaviorRefresh:
	default:
		return nil, sdkerrors.NewCredentialError(sdkconstants.InvalidStaleValueBehavior,
			"staleValueBehavior must be one of BLOCK / ALLOW / REFRESH, got: "+string(staleBehavior), nil)
	}

	p := &IDaaSPamTencentCloudCredentialsProvider{
		roleArn:              cfg.roleArn,
		oidcTokenProvider:    cfg.oidcTokenProvider,
		connectTimeout:       cfg.connectTimeout,
		readTimeout:          cfg.readTimeout,
		idaasInstanceId:      cfg.idaasInstanceId,
		developerApiEndpoint: cfg.developerApiEndpoint,
		// PathEscape 防御性转义 instanceId，避免异常输入破坏 URL path 结构。
		developerApiPath: fmt.Sprintf(adapterconstants.ObtainAccessCredentialPath, url.PathEscape(cfg.idaasInstanceId)),
		httpClient:       newHTTPClient(cfg),
	}

	// 核心 SDK v0.1.0-beta.1 起：refreshFunc 接收 context.Context（取消/deadline 透传），
	// 且 prefetchThreshold 位置参数被移除（预取时机由 StaleTime/PrefetchTime 内部推导）。
	p.supplier = cache.NewCachedResultSupplier(p.refreshCredential, staleBehavior)

	return p, nil
}

// newHTTPClient 解析 HTTP 客户端：优先用注入的（WithHttpClient），否则按核心 SDK
// factory 同款路径自建默认客户端（超时 + 租户级 TLS 开关一并透传）。
func newHTTPClient(cfg *credentialsProviderConfig) sdkhttp.HttpClient {
	if cfg.httpClient != nil {
		return cfg.httpClient
	}
	return sdkhttp.NewDefaultHttpClientWithOptions(cfg.connectTimeout, cfg.readTimeout, cfg.tlsSkipVerify)
}

// validateTimeout 校验超时取值：0 表示未设置（由核心 SDK 回默认值），
// 非零值必须落在 [minTimeoutMs, maxTimeoutMs]，否则按核心 SDK 的错误码返回。
func validateTimeout(field string, value int, code string) error {
	if value == 0 {
		return nil
	}
	if value < minTimeoutMs || value > maxTimeoutMs {
		return sdkerrors.NewCredentialError(code,
			fmt.Sprintf("%s must be in [%d, %d] ms, got: %d", field, minTimeoutMs, maxTimeoutMs, value), nil)
	}
	return nil
}

// GetCredentials 返回腾讯云临时凭证（命中缓存或直接触发刷新）。
//
// 等价于 GetCredentialsWithContext(context.Background())。请求链路中优先使用带 ctx 的
// 版本，以便调用方取消/超时时及时结束等待。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetCredentials() (*domain.TencentCloudCredential, error) {
	return p.GetCredentialsWithContext(context.Background())
}

// GetCredentialsWithContext 返回腾讯云临时凭证，并让调用方的 ctx 参与凭证请求的生命周期。
//
// ctx 约束的是**等待**：核心 SDK 把刷新与单个请求解耦（一次刷新由 leader 触发、在飞的
// 并发请求经 singleflight 合并等待），调用方 ctx 取消/超时时本方法立即返回错误，不再干等
// 刷新完成；在飞的刷新继续执行并把结果写入缓存，供后续请求直接命中。缓存命中、失败降级与
// 过期 fail-fast 语义与 GetCredentials 完全一致。
//
// 关于合并强度的事实边界（勿把"合并"理解成"恰好一次"）：核心 SDK 的 supplier 先在锁外读
// 缓存、再在锁内登记刷新 leader，两步不原子。冷启动并发首取时，多个 goroutine 都可能在
// leader 提交后才进入 doRefresh，发现 in-flight 槽已空 → 自己再当一次 leader。实测
// 16 并发最坏可到 7 次 PAM 调用（理论上界就是并发度：刷新很快时在飞窗口几乎不存在，
// waitInflight 合并路径基本不命中）。这是**仅发生在冷启动的一次性放大**，结果仍是同一份
// 有效凭证；稳态（缓存新鲜）并发读是零 PAM 调用，由单测钉死。
//
// 降级兜底：刷新失败时 CachedResultSupplier 会返回仍在缓冲期内的旧凭证；但当旧凭证已
// 真正过期（PAM 持续故障、缓冲耗尽），不再返回过期凭证，而是 fail-fast 返回
// CredentialExpired —— 否则下游拿到过期凭证只会得到一个难以定位根因的 403。
//
// 因此即便把 WithStaleValueBehavior 配成 ALLOW（"宁可拿过期值也不失败"），本方法仍然拒绝
// 交出已过 Expiration 的凭证——这是适配层的不变量（腾讯云 SDK 拿到过期凭证只会 403，
// 把失败点前移到可归因的 CredentialExpired 更有用）；BLOCK 则按核心 SDK 语义直接返回
// 刷新/退避错误，同样不会拿到旧值。
//
// ctx 为 nil 时按 context.Background() 处理（兼容上游 SDK 传入 nil ctx 的场景）。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetCredentialsWithContext(ctx context.Context) (*domain.TencentCloudCredential, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result, err := p.supplier.GetWithContext(ctx)
	// ErrStaleCredentialReturned 是核心 SDK "刷新失败但把过期旧值交回调用方"的降级信号，
	// 不是硬错误：交给下面的过期校验统一转成 CredentialExpired，保持适配器错误语义稳定。
	if err != nil && !errors.Is(err, cache.ErrStaleCredentialReturned) {
		return nil, err
	}
	// 取时点统一用核心 SDK 的可注入时钟（cache.GetClock()），与 CachedResultSupplier 判定
	// stale/expired 所用的时钟源保持一致；默认时钟即系统时钟，生产行为不变。
	if result == nil || result.Value == nil || !result.Value.Expiration.After(cache.GetClock().Now()) {
		return nil, sdkerrors.NewCredentialError(adapterconstants.CredentialExpired,
			"cached Tencent Cloud credential has expired and PAM refresh is unavailable", err)
	}
	// 返回缓存凭证的副本：result.Value 指向缓存内部同一结构体，若调用方对返回值做字段
	// 赋值（如 cred.SecretId = ...）会直接污染缓存。TencentCloudCredential 字段均为
	// string / time.Time（无可变 slice/map/指针），浅拷贝即足以隔离。代价仅一次小对象拷贝，
	// 相对"缓存被调用方误改导致后续请求拿到脏凭证"的风险可忽略。
	clone := *result.Value
	return &clone, nil
}

// refreshCredential 调用 PAM Developer API 换取新的腾讯云临时凭证（缓存刷新回调）。
//
// ctx 由核心 SDK 的 CachedResultSupplier 注入：同步刷新与后台预取共用同一个"宿主 baseCtx
// + asyncRefreshTimeout"派生的刷新 ctx。本方法把它继续透传给 OIDC 获取与 PAM HTTP 调用，
// 使整条刷新链路受统一超时/取消约束，而不只靠 connect/read 两段分段超时兜底。
func (p *IDaaSPamTencentCloudCredentialsProvider) refreshCredential(
	ctx context.Context,
) (*cache.RefreshResult[*domain.TencentCloudCredential], error) {
	// nil ctx 兜底：refreshFunc 由核心 SDK 调用，正常不会传 nil；防御自定义调用方/测试直调。
	if ctx == nil {
		ctx = context.Background()
	}

	// 1. 从核心 SDK 获取 OIDC Token（AccessToken）作为 Bearer Token。
	//    ctx 交给 provider，使其可取消/限时核心 SDK 内的 Token Endpoint / 元数据服务调用。
	token, err := p.oidcTokenProvider.GetOidcTokenWithContext(ctx)
	if err != nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.OidcTokenError, "failed to get OIDC token", err)
	}
	if token == "" {
		return nil, sdkerrors.NewCredentialError(adapterconstants.OidcTokenError,
			"OIDC token is empty, check core SDK credential provider configuration", nil)
	}
	p.oidcToken.Store(token)

	// 2. 构建 PAM 请求：GET {endpoint}{path}?cloudAccountRoleExternalId={roleArn}
	queryParams := url.Values{}
	queryParams.Set(adapterconstants.CloudAccountRoleExternalId, p.roleArn)

	requestURL := p.developerApiEndpoint + p.developerApiPath

	headers := map[string]string{
		sdkconstants.HeaderAuthorization: "Bearer " + token,
		sdkconstants.HeaderAccept:        sdkconstants.ContentTypeJson,
	}

	httpRequest := &sdkdomain.HttpRequest{
		Method:      enums.HttpMethodGet,
		URL:         requestURL,
		Headers:     headers,
		QueryParams: queryParams,
	}

	// 3. 执行 HTTP 调用（ExecuteContext 透传 ctx，支持请求级取消与 deadline）。
	httpResponse, err := p.httpClient.ExecuteContext(ctx, httpRequest)
	if err != nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.HttpError, "failed to call PAM API", err)
	}

	// 4. 非 2xx：携带状态码与截断后的响应体，便于排障（错误响应体不含已下发凭证）。
	if !httpResponse.IsSuccess() {
		return nil, sdkerrors.NewCredentialError(adapterconstants.PamApiError,
			fmt.Sprintf("PAM API returned status %d: %s", httpResponse.StatusCode, truncateBody(httpResponse.Body)), nil)
	}

	// 5. 解析响应：cloudAccountRoleAccessCredential.tencentCloudStsToken。
	var responseMap map[string]interface{}
	if err := json.Unmarshal(httpResponse.Body, &responseMap); err != nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError, "failed to parse PAM response", err)
	}

	accessCredential, ok := responseMap[adapterconstants.CloudAccountRoleAccessCredential]
	if !ok {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			"PAM response missing '"+adapterconstants.CloudAccountRoleAccessCredential+"' field", nil)
	}

	accessCredentialMap, ok := accessCredential.(map[string]interface{})
	if !ok {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			"PAM response field '"+adapterconstants.CloudAccountRoleAccessCredential+"' is not a JSON object", nil)
	}

	stsToken, ok := accessCredentialMap[adapterconstants.TencentCloudStsToken]
	if !ok {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			"PAM response missing '"+adapterconstants.TencentCloudStsToken+"' field", nil)
	}

	stsTokenMap, ok := stsToken.(map[string]interface{})
	if !ok {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			"PAM response field '"+adapterconstants.TencentCloudStsToken+"' is not a JSON object", nil)
	}

	secretId, _ := stsTokenMap[adapterconstants.TencentCloudSecretId].(string)
	secretKey, _ := stsTokenMap[adapterconstants.TencentCloudSecretKey].(string)
	tokenStr, _ := stsTokenMap[adapterconstants.TencentCloudToken].(string)
	expirationStr, _ := stsTokenMap[adapterconstants.TencentCloudExpiration].(string)

	if secretId == "" || secretKey == "" || tokenStr == "" {
		// 仅说明缺失字段名，不外吐原始 body——2xx 响应体包含已下发的临时凭证，
		// 把整包拼进 error message 会让凭据随错误对象外溢到上游日志/可观测链路。
		var missing []string
		if secretId == "" {
			missing = append(missing, adapterconstants.TencentCloudSecretId)
		}
		if secretKey == "" {
			missing = append(missing, adapterconstants.TencentCloudSecretKey)
		}
		if tokenStr == "" {
			missing = append(missing, adapterconstants.TencentCloudToken)
		}
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			fmt.Sprintf("PAM response has empty Tencent Cloud STS fields: %s", strings.Join(missing, ", ")), nil)
	}

	expiration, err := parseUTCDate(expirationStr)
	if err != nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			fmt.Sprintf("failed to parse expiration date '%s'", expirationStr), err)
	}

	// 6. 计算寿命点。PAM 返回的是绝对过期时间，先换算成以取回时刻为起点的 expiresIn（秒），
	//    再交给核心 SDK 统一构造 RefreshResult —— StaleTime/PrefetchTime/ExpiresAt（含防惊群
	//    jitter）与 Java/Python 及核心 SDK 口径一致，适配器不再各自实现一套。
	//    秒级向下取整只会让 ExpiresAt 略早于真实过期时间（偏安全方向）。
	now := cache.GetClock().Now()
	expiresIn := int64(expiration.Sub(now).Seconds())
	if expiresIn <= 0 {
		return nil, sdkerrors.NewCredentialError(adapterconstants.CredentialExpired,
			fmt.Sprintf("PAM returned an already-expired credential (expiration %s)", expirationStr), nil)
	}

	credential := &domain.TencentCloudCredential{
		SecretId:   secretId,
		SecretKey:  secretKey,
		Token:      tokenStr,
		Expiration: expiration,
	}

	// NewRefreshResult 的 error 分支在理论路径上不可达：expiresIn 已在上面保证 > 0，
	// 而其内部把 expiresIn 钳位到 [1, 1 年] 后必为正值。保留这层兜底而不是写成 panic，
	// 是为了核心 SDK 将来改变钳位区间/校验规则时，适配器仍能返回可归因的错误而不是崩溃。
	// 注意钳位上界 1 年的副作用：若 PAM 给出超过 1 年的 Expiration，ExpiresAt 会早于凭证
	// 真实 Expiration（提前刷新，偏安全方向），当前 PAM STS 寿命 <=12h 不会触发。
	refreshResult, err := cache.NewRefreshResult(credential, now, expiresIn)
	if err != nil {
		return nil, sdkerrors.NewCredentialError(adapterconstants.ParseError,
			fmt.Sprintf("failed to build refresh result for credential expiring at %s", expirationStr), err)
	}
	return &refreshResult, nil
}

// truncateBody 截断响应体用于错误消息，避免超大 body 膨胀日志。
//
// 在 rune 边界截断：byte 切片直接 [:max] 可能把一个多字节 UTF-8 字符从中间切开，
// 产出无效 UTF-8。错误消息会进入日志 / 可观测链路，无效 UTF-8 可能被日志库丢弃
// 或触发编码告警，反而淹没真正的根因。utf8.RuneStart 向前回退到字符起始字节，
// 最多少几个字节，不影响截断目的。非 UTF-8 body（如二进制）按字节回退同样安全。
func truncateBody(body []byte) string {
	if len(body) <= maxErrorBodyLen {
		return string(body)
	}
	cut := maxErrorBodyLen
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return string(body[:cut]) + "...(truncated)"
}

// parseUTCDate 解析 PAM 返回的过期时间（ISO 8601，兼容多种形态）。
//
// 覆盖：标准无小数秒（RFC3339）、带可选小数秒（RFC3339Nano，STS/PAM 类服务常见）、
// 以及空格分隔的非标准形式（含/不含小数秒）。time.RFC3339 不接受小数秒，须显式列出
// time.RFC3339Nano；缺该格式会让刷新持续 ParseError → 缓存耗尽 → 全量 CredentialExpired。
func parseUTCDate(dateStr string) (time.Time, error) {
	if dateStr == "" {
		return time.Time{}, fmt.Errorf("empty expiration date")
	}
	formats := []string{
		time.RFC3339,                    // 2006-01-02T15:04:05Z07:00（无小数秒，覆盖 Z 与 +00:00）
		time.RFC3339Nano,                // 2006-01-02T15:04:05.999999999Z07:00（带可选小数秒）
		"2006-01-02 15:04:05",           // 空格分隔，无小数秒
		"2006-01-02 15:04:05.999999999", // 空格分隔，带可选小数秒
	}
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse date string: %s", dateStr)
}

// Close 释放 provider 持有的资源。
//
// 幂等，可安全多次调用；调用后 provider 仍可继续使用（会按需重新刷新）。
// 核心 SDK v0.1.0-beta.1 起 CachedResultSupplier 暴露 Close()（释放可选的预取策略，
// 未注入策略时为 no-op），同时 http.ResourceCloser 可回收连接池空闲连接。
func (p *IDaaSPamTencentCloudCredentialsProvider) Close() error {
	if p == nil {
		return nil
	}
	if closer, ok := p.httpClient.(sdkhttp.ResourceCloser); ok {
		closer.CloseIdleConnections()
	}
	if p.supplier != nil {
		return p.supplier.Close()
	}
	return nil
}

// GetRoleArn 返回云账号角色外部标识（roleArn）。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetRoleArn() string {
	return p.roleArn
}

// GetOIDCToken 返回最近一次使用的 OIDC Token（仅观测用）。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetOIDCToken() string {
	if v := p.oidcToken.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetIdaasInstanceId 返回 IDaaS 实例 ID。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetIdaasInstanceId() string {
	return p.idaasInstanceId
}

// GetDeveloperApiEndpoint 返回 PAM Developer API 端点。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetDeveloperApiEndpoint() string {
	return p.developerApiEndpoint
}

// GetConnectTimeout 返回连接超时（毫秒）。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetConnectTimeout() int {
	return p.connectTimeout
}

// GetReadTimeout 返回读取超时（毫秒）。
func (p *IDaaSPamTencentCloudCredentialsProvider) GetReadTimeout() int {
	return p.readTimeout
}
