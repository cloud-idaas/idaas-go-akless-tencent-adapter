package main

import (
	"fmt"
	"log"

	"github.com/aliyun/idaas-go-akless-tencent-adapter/pam"
	"github.com/aliyun/idaas-go-core-sdk/config"
	"github.com/aliyun/idaas-go-core-sdk/factory"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
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
	provider, err := pam.GetTencentCloudCredentialsProvider("qcs::cam::uin/100049046297:roleName/user_role")
	if err != nil {
		log.Fatal(err)
	}
	cred, err := provider.GetCredentials()
	if err != nil {
		log.Fatal(err)
	}

	// 3. Use TencentCloud credential to create TencentCloud client
	region := "ap-guangzhou"
	credential := common.NewTokenCredential(cred.SecretId, cred.SecretKey, cred.Token)
	httpProfile := profile.NewHttpProfile()
	httpProfile.Endpoint = "cls.tencentcloudapi.com"
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile = httpProfile
	client, err := cls.NewClient(credential, region, clientProfile)
	if err != nil {
		log.Fatal(err)
	}

	// 4. Use TencentCloud client to call TencentCloud API
	request := cls.NewDescribeTopicsRequest()
	response, err := client.DescribeTopics(request)
	if err != nil {
		log.Fatal(err)
	}
	for _, topic := range response.Response.Topics {
		fmt.Printf("TopicId: %s | TopicName: %s | LogsetId: %s\n",
			*topic.TopicId, *topic.TopicName, *topic.LogsetId)
	}
}
