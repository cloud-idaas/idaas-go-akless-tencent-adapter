package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 腾讯云 SDK（cos-go-sdk-v5 / tencentcloud-sdk-go）只读取这四个字段，
// 取值器语义与字段一一对应是适配的契约面，此处固化以防重构破坏。
func TestTencentCloudCredential_Accessors(t *testing.T) {
	exp := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	c := &TencentCloudCredential{
		SecretId:   "AKID_SECRET_ID",
		SecretKey:  "SECRET_KEY",
		Token:      "SESSION_TOKEN",
		Expiration: exp,
	}

	if got := c.GetSecretId(); got != c.SecretId {
		t.Fatalf("GetSecretId = %q, want %q", got, c.SecretId)
	}
	if got := c.GetSecretKey(); got != c.SecretKey {
		t.Fatalf("GetSecretKey = %q, want %q", got, c.SecretKey)
	}
	if got := c.GetToken(); got != c.Token {
		t.Fatalf("GetToken = %q, want %q", got, c.Token)
	}
	if got := c.GetExpiration(); !got.Equal(exp) {
		t.Fatalf("GetExpiration = %s, want %s", got, exp)
	}
}

func TestTencentCloudCredential_ZeroValueIsUsable(t *testing.T) {
	var c TencentCloudCredential
	if c.GetSecretId() != "" || c.GetSecretKey() != "" || c.GetToken() != "" {
		t.Fatal("zero value credential must expose empty fields")
	}
	if !c.GetExpiration().IsZero() {
		t.Fatal("zero value Expiration must be the zero time")
	}
}

// 凭证结构体经常被宿主直接 %v / %+v / fmt.Println 打进日志。默认输出必须脱敏，
// 否则 SecretKey / SessionToken 明文进日志采集链路（对齐核心 SDK IDaaSCredential
// 的 String() 脱敏兜底设计）。
func TestTencentCloudCredential_StringRedactsSecrets(t *testing.T) {
	c := TencentCloudCredential{
		SecretId:   "AKID_UNIT_TEST_EXAMPLE_VALUE_NOT_A_REAL_KEY",
		SecretKey:  "SECRET_KEY_UNIT_TEST_EXAMPLE_VALUE_NOT_REAL",
		Token:      "SESSION_TOKEN_UNIT_TEST_EXAMPLE_VALUE_NOT_R",
		Expiration: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}

	for _, form := range []string{"%v", "%+v", "%s", "%#v"} {
		out := fmt.Sprintf(form, c)
		assertNoSecretPlaintext(t, form, out, c)
		if !strings.Contains(out, "len=") {
			t.Fatalf("form %s: output should carry redaction markers, got %s", form, out)
		}
	}

	// 指针形式同样脱敏（GetCredentials 返回的就是指针）。
	out := fmt.Sprintf("%+v", &c)
	assertNoSecretPlaintext(t, "*ptr", out, c)

	// 排障需要的信息仍然保留：过期时间可见。
	if !strings.Contains(fmt.Sprintf("%v", c), "2026-10-27T12:00:00Z") {
		t.Fatalf("expiration should remain visible: %s", fmt.Sprintf("%v", c))
	}
}

// 值接收者是刻意的：若改成指针接收者，fmt.Sprintf("%+v", *cred) 会绕过 String()
// 打印明文——这条用例把该回归钉死。
func TestTencentCloudCredential_PointerDerefStillRedacts(t *testing.T) {
	c := &TencentCloudCredential{
		SecretId:   "AKID_UNIT_TEST_EXAMPLE_VALUE_NOT_A_REAL_KEY",
		SecretKey:  "SECRET_KEY_UNIT_TEST_EXAMPLE_VALUE_NOT_REAL",
		Token:      "SESSION_TOKEN_UNIT_TEST_EXAMPLE_VALUE_NOT_R",
		Expiration: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}
	out := fmt.Sprintf("%+v", *c)
	assertNoSecretPlaintext(t, "*deref", out, *c)
}

// 短值（长度 <= 脱敏前缀阈值）不得保留任何明文前缀，也不暴露摘要，
// 避免短 key 空间被穷举还原（核心 SDK logger.Redact 的同款约束）。
func TestTencentCloudCredential_ShortValuesFullyRedacted(t *testing.T) {
	c := TencentCloudCredential{SecretId: "ab", SecretKey: "cd", Token: "ef"}
	out := fmt.Sprintf("%v", c)
	for _, plain := range []string{"ab", "cd", "ef"} {
		if strings.Contains(out, "secretId="+plain) || strings.Contains(out, "secretKey="+plain) || strings.Contains(out, "token="+plain) {
			t.Fatalf("short sensitive value %q leaked in %s", plain, out)
		}
	}
}

func assertNoSecretPlaintext(t *testing.T, form, out string, c TencentCloudCredential) {
	t.Helper()
	for _, secret := range []string{c.SecretId, c.SecretKey, c.Token} {
		if secret == "" {
			continue
		}
		if strings.Contains(out, secret) {
			t.Fatalf("form %s leaked plaintext credential %q in %s", form, secret, out)
		}
	}
}
