package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloud-idaas/idaas-go-akless-tencent-adapter/pam"
	"github.com/cloud-idaas/idaas-go-core-sdk/factory"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// 每步都打进度日志：一旦"看起来没响应"，可以直接从最后一条日志判断卡在配置、PAM 还是腾讯云。
const (
	credentialTimeout = 30 * time.Second
)

// roleArn / region 从环境变量取，让示例无需改代码即可跑真实环境：
//
//	TENCENT_CLOUD_ROLE_ARN —— PAM 中配置的腾讯云账号角色外部标识
//	TENCENT_CLOUD_REGION   —— 地域，默认 ap-guangzhou
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

// describeConfigSource 说明配置将从哪里加载，排障时第一眼就要看到这个。
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

// newClient 每次都用当前凭证新建一个 CLS 客户端。
//
// 为什么这样写：common.NewTokenCredential 返回的是"取一次就固定"的值对象，腾讯云 SDK
// 不会回调本适配器刷新它；而它的字段在签名时被 SDK 无锁读取，后台 goroutine 直接改字段
// 会与在飞请求构成数据竞争。客户端构造很轻，**到期后重建客户端**是 race-free 的姿势。
// COS 侧不同：cos.AuthorizationTransport.SetCredential 自带读写锁，可原地轮换，
// 见 samples/list-cos-buckets。
func newClient(provider *pam.IDaaSPamTencentCloudCredentialsProvider) (*cls.Client, error) {
	cred, err := provider.GetCredentials()
	if err != nil {
		return nil, err
	}

	httpProfile := profile.NewHttpProfile()
	// 用地域化 endpoint（集成实测可用）；全局 cls.tencentcloudapi.com 在部分地域不可路由。
	httpProfile.Endpoint = fmt.Sprintf("cls.%s.tencentcloudapi.com", region())
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile = httpProfile

	// 临时凭证必须带 Token（STS 三元组缺一不可）。
	return cls.NewClient(common.NewTokenCredential(cred.SecretId, cred.SecretKey, cred.Token), region(), clientProfile)
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

	// 4. 调用腾讯云接口 —— 卡在这一步说明是腾讯云 API 侧问题
	client, err := newClient(provider)
	if err != nil {
		log.Fatalf("[4/4] 构建 CLS 客户端失败：%v", err)
	}
	log.Printf("[4/4] 调用 CLS DescribeTopics（endpoint=%s）...", fmt.Sprintf("cls.%s.tencentcloudapi.com", region()))
	response, err := client.DescribeTopics(cls.NewDescribeTopicsRequest())
	if err != nil {
		log.Fatalf("[4/4] DescribeTopics 失败：%v", err)
	}
	log.Printf("[4/4] 成功，返回 %d 个 topic", len(response.Response.Topics))
	for _, topic := range response.Response.Topics {
		fmt.Printf("TopicId: %s | TopicName: %s | LogsetId: %s\n",
			*topic.TopicId, *topic.TopicName, *topic.LogsetId)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
