package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	"github.com/tencentyun/cos-go-sdk-v5"
)

const (
	credentialTimeout = 30 * time.Second
	rotateInterval    = 5 * time.Minute
)

// roleArn / region 的读取姿势与 list-cls-topics 一致：环境变量优先，未设置地域则默认 ap-guangzhou。
func roleArn() string {
	v := os.Getenv("TENCENT_CLOUD_ROLE_ARN")
	if v == "" {
		log.Fatal("缺 TENCENT_CLOUD_ROLE_ARN：请先 export TENCENT_CLOUD_ROLE_ARN='qcs::cam::uin/<uin>:roleName/<role>'（PAM 中配置的腾讯云账号角色外部标识）")
	}
	return v
}

func region() string {
	if v := os.Getenv("TENCENT_CLOUD_REGION"); v != "" {
		return v
	}
	return "ap-guangzhou"
}

func describeConfigSource() string {
	if p := os.Getenv("CLOUD_IDAAS_CONFIG_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return fmt.Sprintf("%s（CLOUD_IDAAS_CONFIG_PATH，文件不可读：%v）", p, err)
		}
		return p + "（CLOUD_IDAAS_CONFIG_PATH）"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloud_idaas/client-config.json") + "（默认路径，未设 CLOUD_IDAAS_CONFIG_PATH）"
}

// refreshTransport 把适配器当前凭证写入 COS 传输层。
//
// cos.AuthorizationTransport 用读写锁保护凭证字段（SetCredential/GetCredential），
// 因此可以在后台 goroutine 里原地轮换，无需重建 client。适配器自带缓存，
// GetCredentials 命中缓存时不发网络请求，周期调用很轻。
func refreshTransport(ctx context.Context, provider *pam.IDaaSPamTencentCloudCredentialsProvider, transport *cos.AuthorizationTransport) error {
	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		return err
	}
	transport.SetCredential(cred.SecretId, cred.SecretKey, cred.Token)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// 1. Initialize IDaaS Core SDK
	// factory.Init() 按三级优先级加载配置（CLOUD_IDAAS_CONFIG_PATH ->
	// ~/.cloud_idaas/client-config.json）；GetInstance()/Initialize() 在核心 SDK
	// v0.1.0-beta.1 中已 Deprecated，改用包级 Init。
	log.Printf("[1/4] 初始化 IDaaS Core SDK，配置来源：%s", describeConfigSource())
	if err := factory.Init(); err != nil {
		log.Fatalf("[1/4] 初始化失败：%v", err)
	}

	// 2. Create TencentCloud credentials provider via factory
	log.Printf("[2/4] 创建腾讯云凭证提供者，roleArn=%s region=%s", roleArn(), region())
	provider, err := pam.GetTencentCloudCredentialsProvider(roleArn())
	if err != nil {
		log.Fatalf("[2/4] 创建提供者失败：%v", err)
	}
	defer provider.Close()
	log.Printf("[2/4] 提供者就绪，PAM endpoint=%s，超时 connect=%dms read=%dms",
		provider.GetDeveloperApiEndpoint(), provider.GetConnectTimeout(), provider.GetReadTimeout())

	// 3. 换取 STS —— 卡在这一步说明是 IDaaS/PAM 侧网络或配置问题
	ctx, cancel := context.WithTimeout(context.Background(), credentialTimeout)
	defer cancel()
	start := time.Now()
	log.Printf("[3/4] 调用 PAM 换取腾讯云 STS（最长等 %s）...", credentialTimeout)
	cred, err := provider.GetCredentialsWithContext(ctx)
	if err != nil {
		log.Fatalf("[3/4] 换取凭证失败（耗时 %s）：%v", time.Since(start).Round(time.Millisecond), err)
	}
	log.Printf("[3/4] 凭证到手，耗时 %s；SecretId=%s... 过期时间=%s",
		time.Since(start).Round(time.Millisecond), firstN(cred.SecretId, 6), cred.Expiration)

	// 4. Use TencentCloud credential to create TencentCloud client
	u, _ := url.Parse(fmt.Sprintf("https://cos.%s.myqcloud.com", region()))
	b := &cos.BaseURL{BucketURL: u}
	transport := &cos.AuthorizationTransport{}
	client := cos.NewClient(b, &http.Client{Transport: transport})

	transport.SetCredential(cred.SecretId, cred.SecretKey, cred.Token)

	// 长跑进程：后台按固定周期把适配器凭证同步进 transport（适配器会在 2/3 寿命点预取、
	// 4/5 寿命点同步刷新，这里只需保证 transport 跟得上）。一次性 CLI 可删掉这段。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(rotateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := refreshTransport(context.Background(), provider, transport); err != nil {
					log.Printf("后台轮换 COS 凭证失败（不致命，下次会重试）：%v", err)
				}
			case <-stop:
				return
			}
		}
	}()

	log.Printf("[4/4] 调用 COS Service.Get 列举 bucket（endpoint=%s）...", u)
	result, _, err := client.Service.Get(context.Background())
	if err != nil {
		log.Fatalf("[4/4] 列举 bucket 失败：%v", err)
	}
	log.Printf("[4/4] 成功，返回 %d 个 bucket", len(result.Buckets))
	for _, bucket := range result.Buckets {
		fmt.Printf("Bucket: %s | Region: %s | CreateDate: %s\n",
			bucket.Name, bucket.Region, bucket.CreationDate)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
