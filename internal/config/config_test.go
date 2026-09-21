package config

import "testing"

func TestAuthDisabledRequiresExplicitValidBoolean(t *testing.T) {
	t.Setenv("AUTH_DISABLED", "true")
	cfg, err := Load("payment-api")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AuthDisabled {
		t.Fatal("AuthDisabled = false, want true")
	}

	t.Setenv("AUTH_DISABLED", "definitely")
	if _, err := Load("payment-api"); err == nil {
		t.Fatal("Load() with invalid AUTH_DISABLED returned nil error")
	}
}

func TestAuthenticationDefaultsToEnabled(t *testing.T) {
	t.Setenv("AUTH_DISABLED", "")
	cfg, err := Load("payment-api")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AuthDisabled {
		t.Fatal("AuthDisabled = true, want secure default")
	}
	if cfg.AuthRequiredRole != "payment-user" {
		t.Fatalf("AuthRequiredRole = %q, want payment-user", cfg.AuthRequiredRole)
	}
}

func TestManagedServiceTransportConfiguration(t *testing.T) {
	t.Setenv("REDIS_USERNAME", "payment")
	t.Setenv("REDIS_PASSWORD", "secret")
	t.Setenv("REDIS_TLS_ENABLED", "true")
	t.Setenv("KAFKA_TLS_ENABLED", "true")
	t.Setenv("AWS_ENDPOINT", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	cfg, err := Load("payment-api")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RedisTLSEnabled || !cfg.KafkaTLSEnabled {
		t.Fatalf("managed TLS flags were not loaded: %+v", cfg)
	}
	if cfg.RedisUsername != "payment" || cfg.RedisPassword != "secret" {
		t.Fatal("Redis credentials were not loaded")
	}
	if cfg.AWSEndpoint != "" || cfg.AWSAccessKey != "" || cfg.AWSSecretKey != "" {
		t.Fatal("native AWS mode must not synthesize a LocalStack endpoint or static credentials")
	}
}

func TestRejectsInvalidManagedServiceBooleansAndPartialAWSCredentials(t *testing.T) {
	t.Setenv("REDIS_TLS_ENABLED", "sometimes")
	if _, err := Load("payment-api"); err == nil {
		t.Fatal("expected invalid REDIS_TLS_ENABLED to fail")
	}

	t.Setenv("REDIS_TLS_ENABLED", "false")
	t.Setenv("AWS_ACCESS_KEY_ID", "partial")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, err := Load("payment-api"); err == nil {
		t.Fatal("expected partial AWS credentials to fail")
	}
}
