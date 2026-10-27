package constants

import (
	"strings"
	"testing"
)

// 这些字面量是与 PAM Developer API、以及 Java / Python / 其他云适配器共享的**线上契约**：
// 改任何一条都等于改协议，必须跨语言协同并与 PAM 服务确认后在 CHANGELOG 里标为 breaking。
// 本用例的作用是把它们钉住，让无意的改动在 `go test` 阶段就失败，而不是在运行期收到 404。
func TestWireContractPinned(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"ObtainAccessCredentialPath", ObtainAccessCredentialPath,
			"/v2/%s/cloudAccountRoles/_/actions/obtainAccessCredential"},
		{"CloudAccountRoleExternalId", CloudAccountRoleExternalId, "cloudAccountRoleExternalId"},
		{"CloudAccountRoleAccessCredential", CloudAccountRoleAccessCredential, "cloudAccountRoleAccessCredential"},
		{"TencentCloudStsToken", TencentCloudStsToken, "tencentCloudStsToken"},
		{"TencentCloudSecretId", TencentCloudSecretId, "tmpSecretId"},
		{"TencentCloudSecretKey", TencentCloudSecretKey, "tmpSecretKey"},
		{"TencentCloudToken", TencentCloudToken, "token"},
		{"TencentCloudExpiration", TencentCloudExpiration, "expiration"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q（线上契约变更需跨语言协同）", c.name, c.got, c.want)
		}
	}
}

// 错误码是上游做归因与重试策略的依据，同样按码值钉死。
func TestErrorCodesPinned(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"InvalidParameter", InvalidParameter, "InvalidParameter"},
		{"OidcTokenError", OidcTokenError, "OidcTokenError"},
		{"HttpError", HttpError, "HttpError"},
		{"PamApiError", PamApiError, "PamApiError"},
		{"ParseError", ParseError, "ParseError"},
		{"CredentialExpired", CredentialExpired, "CredentialExpired"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// ObtainAccessCredentialPath 只有一个 %s 占位（instanceId），且必须以 / 开头，
// 否则适配器拼出的 URL 结构会被破坏（例如多出一个占位会让 fmt.Sprintf 少参数、直接报错）。
func TestObtainAccessCredentialPathShape(t *testing.T) {
	if got := strings.Count(ObtainAccessCredentialPath, "%"); got != 1 {
		t.Fatalf("ObtainAccessCredentialPath 中 %% 出现 %d 次，应只有 1 个 %%s 占位: %q", got, ObtainAccessCredentialPath)
	}
	if got := strings.Count(ObtainAccessCredentialPath, "%s"); got != 1 {
		t.Fatalf("ObtainAccessCredentialPath 占位符数量 = %d, want 1", got)
	}
	if !strings.HasPrefix(ObtainAccessCredentialPath, "/") {
		t.Fatalf("ObtainAccessCredentialPath 必须以 / 开头: %q", ObtainAccessCredentialPath)
	}
}
