package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/aliyun/idaas-go-akless-tencent-adapter/pam"
	"github.com/aliyun/idaas-go-core-sdk/config"
	"github.com/aliyun/idaas-go-core-sdk/factory"
	"github.com/tencentyun/cos-go-sdk-v5"
)

func main() {
	// 1. Initialize IDaaS Core SDK
	reader := config.NewConfigReader()
	cfg, err := reader.LoadWithPriority("")
	if err != nil {
		panic(err)
	}
	factoryInstance := factory.GetInstance()
	err = factoryInstance.Initialize(cfg)
	if err != nil {
		panic(err)
	}

	// 2. Create TencentCloud credentials provider via factory
	provider, err := pam.GetTencentCloudCredentialsProvider("your-role-arn")
	if err != nil {
		log.Fatal(err)
	}
	cred, err := provider.GetCredentials()
	if err != nil {
		log.Fatal(err)
	}

	// 3. Use TencentCloud credential to create TencentCloud client
	u, _ := url.Parse(fmt.Sprintf("https://cos.%s.myqcloud.com", "ap-guangzhou"))
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:     cred.SecretId,
			SecretKey:    cred.SecretKey,
			SessionToken: cred.Token,
		},
	})

	// 4. Use TencentCloud client to call TencentCloud API
	result, _, err := client.Service.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, bucket := range result.Buckets {
		fmt.Printf("Bucket: %s | Region: %s | CreateDate: %s\n",
			bucket.Name, bucket.Region, bucket.CreationDate)
	}
}
