package domain

import "time"

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
