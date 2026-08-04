package pam

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

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

const (
	defaultConnectTimeout = 5000
	defaultReadTimeout    = 10000
)

type IDaaSPamTencentCloudCredentialsProvider struct {
	roleArn              string
	oidcToken            string
	oidcTokenProvider    provider.OidcTokenProvider
	connectTimeout       int
	readTimeout          int
	idaasInstanceId      string
	developerApiEndpoint string
	developerApiPath     string
	httpClient           sdkhttp.HttpClient
	supplier             *cache.CachedResultSupplier[*domain.TencentCloudCredential]
}

type CredentialsProviderOption func(*credentialsProviderConfig)

type credentialsProviderConfig struct {
	developerApiEndpoint string
	idaasInstanceId      string
	oidcTokenProvider    provider.OidcTokenProvider
	roleArn              string
	connectTimeout       int
	readTimeout          int
}

func WithDeveloperApiEndpoint(endpoint string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.developerApiEndpoint = endpoint
	}
}

func WithIdaasInstanceId(instanceId string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.idaasInstanceId = instanceId
	}
}

func WithOidcTokenProvider(provider provider.OidcTokenProvider) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.oidcTokenProvider = provider
	}
}

func WithRoleArn(roleArn string) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.roleArn = roleArn
	}
}

func WithConnectTimeout(timeout int) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.connectTimeout = timeout
	}
}

func WithReadTimeout(timeout int) CredentialsProviderOption {
	return func(c *credentialsProviderConfig) {
		c.readTimeout = timeout
	}
}

func NewIDaaSPamTencentCloudCredentialsProvider(opts ...CredentialsProviderOption) (*IDaaSPamTencentCloudCredentialsProvider, error) {
	cfg := &credentialsProviderConfig{
		connectTimeout: defaultConnectTimeout,
		readTimeout:    defaultReadTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.roleArn == "" {
		return nil, sdkerrors.NewCredentialError("InvalidParameter", "roleArn cannot be empty", nil)
	}
	if cfg.oidcTokenProvider == nil {
		return nil, sdkerrors.NewCredentialError("InvalidParameter", "oidcTokenProvider cannot be nil", nil)
	}
	if cfg.idaasInstanceId == "" {
		return nil, sdkerrors.NewCredentialError("InvalidParameter", "idaasInstanceId cannot be empty", nil)
	}
	if cfg.developerApiEndpoint == "" {
		return nil, sdkerrors.NewCredentialError("InvalidParameter", "developerApiEndpoint cannot be empty", nil)
	}

	p := &IDaaSPamTencentCloudCredentialsProvider{
		roleArn:              cfg.roleArn,
		oidcTokenProvider:    cfg.oidcTokenProvider,
		connectTimeout:       cfg.connectTimeout,
		readTimeout:          cfg.readTimeout,
		idaasInstanceId:      cfg.idaasInstanceId,
		developerApiEndpoint: cfg.developerApiEndpoint,
		developerApiPath:     fmt.Sprintf(adapterconstants.ObtainAccessCredentialPath, cfg.idaasInstanceId),
		httpClient:           sdkhttp.NewDefaultHttpClient(cfg.connectTimeout, cfg.readTimeout),
	}

	p.supplier = cache.NewCachedResultSupplier(
		p.refreshCredential,
		enums.StaleValueBehaviorRefresh,
		0,
	)

	return p, nil
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetCredentials() (*domain.TencentCloudCredential, error) {
	result, err := p.supplier.Get()
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (p *IDaaSPamTencentCloudCredentialsProvider) refreshCredential() (*cache.RefreshResult[*domain.TencentCloudCredential], error) {
	token, err := p.oidcTokenProvider.GetOidcToken()
	if err != nil {
		return nil, sdkerrors.NewCredentialError("OidcTokenError", "failed to get OIDC token", err)
	}
	p.oidcToken = token

	queryParams := url.Values{}
	queryParams.Set(adapterconstants.CloudAccountRoleExternalId, p.roleArn)

	requestURL := p.developerApiEndpoint + p.developerApiPath

	headers := map[string]string{
		sdkconstants.HeaderAuthorization: "Bearer " + token,
	}

	httpRequest := &sdkdomain.HttpRequest{
		Method:      enums.HttpMethodGet,
		URL:         requestURL,
		Headers:     headers,
		QueryParams: queryParams,
	}

	httpResponse, err := p.httpClient.Execute(httpRequest)
	if err != nil {
		return nil, sdkerrors.NewCredentialError("HttpError", "failed to call PAM API", err)
	}

	if !httpResponse.IsSuccess() {
		return nil, sdkerrors.NewCredentialError("PamApiError",
			fmt.Sprintf("PAM API returned status %d: %s", httpResponse.StatusCode, string(httpResponse.Body)), nil)
	}

	var responseMap map[string]interface{}
	if err := json.Unmarshal(httpResponse.Body, &responseMap); err != nil {
		return nil, sdkerrors.NewCredentialError("ParseError", "failed to parse PAM response", err)
	}

	accessCredential, ok := responseMap[adapterconstants.CloudAccountRoleAccessCredential]
	if !ok {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("error retrieving credentials from PAM result: %s", string(httpResponse.Body)), nil)
	}

	accessCredentialMap, ok := accessCredential.(map[string]interface{})
	if !ok {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("error retrieving credentials from PAM result: %s", string(httpResponse.Body)), nil)
	}

	stsToken, ok := accessCredentialMap[adapterconstants.TencentCloudStsToken]
	if !ok {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("error retrieving credentials from PAM result: %s", string(httpResponse.Body)), nil)
	}

	stsTokenMap, ok := stsToken.(map[string]interface{})
	if !ok {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("error retrieving credentials from PAM result: %s", string(httpResponse.Body)), nil)
	}

	secretId, _ := stsTokenMap[adapterconstants.TencentCloudSecretId].(string)
	secretKey, _ := stsTokenMap[adapterconstants.TencentCloudSecretKey].(string)
	tokenStr, _ := stsTokenMap[adapterconstants.TencentCloudToken].(string)
	expirationStr, _ := stsTokenMap[adapterconstants.TencentCloudExpiration].(string)

	if secretId == "" || secretKey == "" || tokenStr == "" {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("error retrieving credentials from PAM result: %s", string(httpResponse.Body)), nil)
	}

	expiration, err := parseUTCDate(expirationStr)
	if err != nil {
		return nil, sdkerrors.NewCredentialError("ParseError",
			fmt.Sprintf("failed to parse expiration date '%s'", expirationStr), err)
	}

	now := time.Now()
	expiresIn := expiration.Sub(now)

	prefetchTime := expiration.Add(-expiresIn / 3)

	credential := &domain.TencentCloudCredential{
		SecretId:   secretId,
		SecretKey:  secretKey,
		Token:      tokenStr,
		Expiration: expiration,
	}

	return &cache.RefreshResult[*domain.TencentCloudCredential]{
		Value:     credential,
		ExpiresAt: prefetchTime,
	}, nil
}

func parseUTCDate(dateStr string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05+00:00",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse date string: %s", dateStr)
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetRoleArn() string {
	return p.roleArn
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetOIDCToken() string {
	return p.oidcToken
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetIdaasInstanceId() string {
	return p.idaasInstanceId
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetDeveloperApiEndpoint() string {
	return p.developerApiEndpoint
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetConnectTimeout() int {
	return p.connectTimeout
}

func (p *IDaaSPamTencentCloudCredentialsProvider) GetReadTimeout() int {
	return p.readTimeout
}
