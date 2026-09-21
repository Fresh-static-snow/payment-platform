package search

import (
	"os"
	"strconv"
	"strings"
)

type ServiceConfig struct {
	ElasticsearchURL      string
	ElasticsearchIndex    string
	ElasticsearchAlias    string
	ElasticsearchUsername string
	ElasticsearchPassword string
	Workers               int
}

func LoadServiceConfig() ServiceConfig {
	workers, err := strconv.Atoi(environment("SEARCH_WORKER_COUNT", "1"))
	if err != nil || workers < 1 || workers > 32 {
		workers = 1
	}
	return ServiceConfig{
		ElasticsearchURL:      environment("ELASTICSEARCH_URL", "http://localhost:9200"),
		ElasticsearchIndex:    environment("ELASTICSEARCH_INDEX", "payments-read-v1"),
		ElasticsearchAlias:    environment("ELASTICSEARCH_ALIAS", "payments-read"),
		ElasticsearchUsername: strings.TrimSpace(os.Getenv("ELASTICSEARCH_USERNAME")),
		ElasticsearchPassword: os.Getenv("ELASTICSEARCH_PASSWORD"),
		Workers:               workers,
	}
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
