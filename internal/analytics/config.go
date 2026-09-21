package analytics

import (
	"os"
	"strconv"
	"strings"
)

type ServiceConfig struct {
	URL, Database, Table, Username, Password string
	Workers                                  int
}

func LoadServiceConfig() ServiceConfig {
	workers, err := strconv.Atoi(env("ANALYTICS_WORKER_COUNT", "1"))
	if err != nil || workers < 1 || workers > 32 {
		workers = 1
	}
	return ServiceConfig{
		URL: env("CLICKHOUSE_URL", "http://localhost:8123"), Database: env("CLICKHOUSE_DATABASE", "payment_analytics"),
		Table: env("CLICKHOUSE_PAYMENT_EVENTS_TABLE", "payment_events"), Username: env("CLICKHOUSE_USERNAME", "default"),
		Password: os.Getenv("CLICKHOUSE_PASSWORD"), Workers: workers,
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
