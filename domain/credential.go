package domain

import (
	"strconv"
	"time"

	"github.com/cloud-idaas/idaas-go-core-sdk/logger"
)

// TencentCloudCredential 腾讯云 STS 临时凭证。
//
// 字段可被调用方读取（腾讯云 SDK 需要 SecretId/SecretKey/Token 明文），
// 但**打印**默认脱敏，见 String。
type TencentCloudCredential struct {
	SecretId   string
	SecretKey  string
	Token      string
	Expiration time.Time
}

func (c *TencentCloudCredential) GetSecretId() string {
	return c.SecretId
}

func (c *TencentCloudCredential) GetSecretKey() string {
	return c.SecretKey
}

func (c *TencentCloudCredential) GetToken() string {
	return c.Token
}

func (c *TencentCloudCredential) GetExpiration() time.Time {
	return c.Expiration
}

// String 返回脱敏摘要，避免凭证随 %v / %s / fmt.Println 明文落到日志或可观测链路。
//
// 与核心 SDK credential.IDaaSCredential.String() 同款兜底思路：明文一律不出现，
// 只保留前缀 + 长度 + SHA256 前 8 位（可在多行日志间关联比对，又不足以反推原文）。
// 用**值接收者**而非指针接收者：这样 %v 传值或传指针都走脱敏分支——指针接收者下
// `fmt.Printf("%+v", *cred)`（解引用后传值）会绕过 String() 直接打印全部字段。
func (c TencentCloudCredential) String() string {
	return "TencentCloudCredential{secretId=" + logger.Redact(c.SecretId) +
		", secretKey=" + logger.Redact(c.SecretKey) +
		", token=" + logger.Redact(c.Token) +
		", expiration=" + c.Expiration.Format(time.RFC3339) +
		", expiresIn=" + strconv.FormatInt(int64(time.Until(c.Expiration).Seconds()), 10) + "s}"
}

// GoString 同样脱敏：%#v 常被用于"打印完整结构体"的调试路径，只实现 String()
// 会被 %#v 绕过。
func (c TencentCloudCredential) GoString() string {
	return c.String()
}
