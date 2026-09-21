package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServiceName      string
	HTTPPort         string
	GRPCPort         string
	DatabaseURL      string
	RedisAddr        string
	RedisUsername    string
	RedisPassword    string
	RedisTLSEnabled  bool
	KafkaBrokers     []string
	KafkaTLSEnabled  bool
	RiskAddress      string
	AWSEndpoint      string
	AWSRegion        string
	AWSAccessKey     string
	AWSSecretKey     string
	S3Bucket         string
	SNSTopicARN      string
	SQSQueueURL      string
	WorkerCount      int
	ProviderTimeout  time.Duration
	ShutdownTimeout  time.Duration
	OTLPEndpoint     string
	AuthDisabled     bool
	AuthIssuerURL    string
	AuthAudience     string
	AuthJWKSURL      string
	AuthRequiredRole string
	AuthRoleClientID string
}

func Load(serviceName string) (Config, error) {
	authDisabled, err := envBool("AUTH_DISABLED", false)
	if err != nil {
		return Config{}, err
	}
	redisTLSEnabled, err := envBool("REDIS_TLS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	kafkaTLSEnabled, err := envBool("KAFKA_TLS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ServiceName:      serviceName,
		HTTPPort:         env("HTTP_PORT", "8080"),
		GRPCPort:         env("GRPC_PORT", "9090"),
		DatabaseURL:      env("DATABASE_URL", "postgres://payment:payment@localhost:5432/payments?sslmode=disable"),
		RedisAddr:        env("REDIS_ADDR", "localhost:6379"),
		RedisUsername:    env("REDIS_USERNAME", ""),
		RedisPassword:    env("REDIS_PASSWORD", ""),
		RedisTLSEnabled:  redisTLSEnabled,
		KafkaBrokers:     split(env("KAFKA_BROKERS", "localhost:9092")),
		KafkaTLSEnabled:  kafkaTLSEnabled,
		RiskAddress:      env("RISK_GRPC_ADDR", "localhost:9090"),
		AWSEndpoint:      env("AWS_ENDPOINT", ""),
		AWSRegion:        env("AWS_REGION", "us-east-1"),
		AWSAccessKey:     env("AWS_ACCESS_KEY_ID", ""),
		AWSSecretKey:     env("AWS_SECRET_ACCESS_KEY", ""),
		S3Bucket:         env("S3_BUCKET", "payment-receipts"),
		SNSTopicARN:      env("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:payment-notifications"),
		SQSQueueURL:      env("SQS_QUEUE_URL", "http://localhost:4566/000000000000/payment-notifications"),
		WorkerCount:      envInt("WORKER_COUNT", 4),
		ProviderTimeout:  envDuration("PROVIDER_TIMEOUT", 2*time.Second),
		ShutdownTimeout:  envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		OTLPEndpoint:     env("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317"),
		AuthDisabled:     authDisabled,
		AuthIssuerURL:    env("AUTH_ISSUER_URL", ""),
		AuthAudience:     env("AUTH_AUDIENCE", ""),
		AuthJWKSURL:      env("AUTH_JWKS_URL", ""),
		AuthRequiredRole: env("AUTH_REQUIRED_ROLE", "payment-user"),
		AuthRoleClientID: env("AUTH_ROLE_CLIENT_ID", ""),
	}
	if cfg.WorkerCount < 1 || cfg.WorkerCount > 128 {
		return Config{}, fmt.Errorf("WORKER_COUNT must be between 1 and 128")
	}
	if len(cfg.KafkaBrokers) == 0 {
		return Config{}, fmt.Errorf("KAFKA_BROKERS must not be empty")
	}
	if (cfg.AWSAccessKey == "") != (cfg.AWSSecretKey == "") {
		return Config{}, fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be configured together")
	}
	return cfg, nil
}

func envBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return value, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(env(name, strconv.Itoa(fallback)))
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(env(name, fallback.String()))
	if err != nil {
		return fallback
	}
	return value
}

func split(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
