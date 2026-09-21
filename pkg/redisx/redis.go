package redisx

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	Address    string
	Username   string
	Password   string
	TLSEnabled bool
}

func Open(ctx context.Context, address string) (*redis.Client, error) {
	return OpenWithConfig(ctx, Config{Address: address})
}

func OpenWithConfig(ctx context.Context, cfg Config) (*redis.Client, error) {
	options := &redis.Options{
		Addr: cfg.Address, Username: cfg.Username, Password: cfg.Password,
		PoolSize: 20, MinIdleConns: 2,
	}
	if cfg.TLSEnabled {
		host, _, err := net.SplitHostPort(cfg.Address)
		if err != nil {
			return nil, fmt.Errorf("parse Redis address for TLS: %w", err)
		}
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
