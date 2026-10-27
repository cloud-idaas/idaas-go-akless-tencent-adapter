package constants

// 适配器对外暴露的错误码（与 idaas-go-akless-alibabacloud-adapter 的码值对齐，
// 便于上游按码值统一归因与重试策略）。
const (
	// InvalidParameter 必填参数缺失或非法。
	InvalidParameter = "InvalidParameter"

	// OidcTokenError 从核心 SDK 获取 OIDC Token 失败。
	OidcTokenError = "OidcTokenError"

	// HttpError 调用 PAM Developer API 时发生传输层错误。
	HttpError = "HttpError"

	// PamApiError PAM Developer API 返回非 2xx 响应。
	PamApiError = "PamApiError"

	// ParseError PAM 响应解析失败或缺少必需字段。
	ParseError = "ParseError"

	// CredentialExpired 缓存的腾讯云临时凭证已真正过期且 PAM 刷新不可用（降级缓冲耗尽）。
	// 与 PamApiError 区分：后者表示 PAM 返回了非 2xx，本码表示降级兜底已耗尽、
	// 不能再把过期凭证交给下游（否则下游只看到一个难以定位根因的 403）。
	CredentialExpired = "CredentialExpired"
)
