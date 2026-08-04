package pam

import (
	"github.com/aliyun/idaas-go-core-sdk/factory"
	"github.com/aliyun/idaas-go-core-sdk/provider"
)

type oidcTokenAdapter struct {
	credProvider provider.IDaaSCredentialProvider
}

func (a *oidcTokenAdapter) GetOidcToken() (string, error) {
	cred, err := a.credProvider.GetCredential()
	if err != nil {
		return "", err
	}
	return cred.GetAccessToken(), nil
}

func GetTencentCloudCredentialsProvider(roleArn string) (*IDaaSPamTencentCloudCredentialsProvider, error) {
	f := factory.GetInstance()
	cfg := f.GetConfig()

	credProvider, err := f.CreateCredentialProvider()
	if err != nil {
		return nil, err
	}

	return NewIDaaSPamTencentCloudCredentialsProvider(
		WithOidcTokenProvider(&oidcTokenAdapter{credProvider: credProvider}),
		WithDeveloperApiEndpoint(cfg.DeveloperApiEndpoint),
		WithIdaasInstanceId(cfg.InstanceId),
		WithRoleArn(roleArn),
	)
}

