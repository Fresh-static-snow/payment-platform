package search

import "testing"

func TestLoadServiceConfig(t *testing.T) {
	t.Setenv("ELASTICSEARCH_URL", "http://elasticsearch:9200")
	t.Setenv("ELASTICSEARCH_INDEX", "payments-v2")
	t.Setenv("ELASTICSEARCH_ALIAS", "payments")
	t.Setenv("ELASTICSEARCH_USERNAME", "indexer")
	t.Setenv("ELASTICSEARCH_PASSWORD", "secret")
	t.Setenv("SEARCH_WORKER_COUNT", "6")

	config := LoadServiceConfig()
	if config.ElasticsearchURL != "http://elasticsearch:9200" || config.ElasticsearchIndex != "payments-v2" || config.ElasticsearchAlias != "payments" {
		t.Fatalf("unexpected Elasticsearch config: %+v", config)
	}
	if config.ElasticsearchUsername != "indexer" || config.ElasticsearchPassword != "secret" || config.Workers != 6 {
		t.Fatalf("unexpected service config: %+v", config)
	}
}

func TestLoadServiceConfigFallsBackForInvalidWorkerCount(t *testing.T) {
	t.Setenv("SEARCH_WORKER_COUNT", "0")
	if config := LoadServiceConfig(); config.Workers != 1 {
		t.Fatalf("workers = %d, want 1", config.Workers)
	}
}
