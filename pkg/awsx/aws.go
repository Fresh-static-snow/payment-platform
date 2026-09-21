package awsx

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type Clients struct {
	S3  *s3.Client
	SNS *sns.Client
	SQS *sqs.Client
}

func New(ctx context.Context, endpoint, region, accessKey, secretKey string) (Clients, error) {
	endpoint = strings.TrimSpace(endpoint)
	accessKey = strings.TrimSpace(accessKey)
	secretKey = strings.TrimSpace(secretKey)
	if (accessKey == "") != (secretKey == "") {
		return Clients{}, fmt.Errorf("AWS access key and secret key must be configured together")
	}
	options := []func(*config.LoadOptions) error{config.WithRegion(strings.TrimSpace(region))}
	if accessKey != "" {
		// Static credentials are intentionally limited to LocalStack and explicit
		// development overrides. In AWS, leaving both values empty activates the
		// standard credential chain (including EKS IRSA web identity tokens).
		options = append(options, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}
	cfg, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return Clients{}, fmt.Errorf("load AWS config: %w", err)
	}
	return Clients{
		S3: s3.NewFromConfig(cfg, func(options *s3.Options) {
			if endpoint != "" {
				options.BaseEndpoint = aws.String(endpoint)
				options.UsePathStyle = true
			}
		}),
		SNS: sns.NewFromConfig(cfg, func(options *sns.Options) {
			if endpoint != "" {
				options.BaseEndpoint = aws.String(endpoint)
			}
		}),
		SQS: sqs.NewFromConfig(cfg, func(options *sqs.Options) {
			if endpoint != "" {
				options.BaseEndpoint = aws.String(endpoint)
			}
		}),
	}, nil
}
