package analytics

import "testing"

func TestLoadServiceConfig(t *testing.T) {
	t.Setenv("CLICKHOUSE_URL", "http://clickhouse:8123")
	t.Setenv("CLICKHOUSE_DATABASE", "analytics_test")
	t.Setenv("CLICKHOUSE_PAYMENT_EVENTS_TABLE", "events_test")
	t.Setenv("CLICKHOUSE_USERNAME", "writer")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	t.Setenv("ANALYTICS_WORKER_COUNT", "4")

	config := LoadServiceConfig()
	if config.URL != "http://clickhouse:8123" || config.Database != "analytics_test" || config.Table != "events_test" {
		t.Fatalf("unexpected storage config: %+v", config)
	}
	if config.Username != "writer" || config.Password != "secret" || config.Workers != 4 {
		t.Fatalf("unexpected client config: %+v", config)
	}
}

func TestLoadServiceConfigFallsBackForInvalidWorkerCount(t *testing.T) {
	t.Setenv("ANALYTICS_WORKER_COUNT", "999")
	if config := LoadServiceConfig(); config.Workers != 1 {
		t.Fatalf("workers = %d, want 1", config.Workers)
	}
}
