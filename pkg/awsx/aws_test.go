package awsx

import (
	"context"
	"testing"
)

func TestNewUsesNativeAWSDefaultsWhenEndpointAndStaticCredentialsAreEmpty(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	clients, err := New(context.Background(), "", "us-east-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := clients.S3.Options().BaseEndpoint; got != nil {
		t.Fatalf("S3 BaseEndpoint = %q, want native AWS endpoint resolution", *got)
	}
	if clients.S3.Options().UsePathStyle {
		t.Fatal("native AWS S3 client unexpectedly uses path-style addressing")
	}
	if got := clients.SNS.Options().BaseEndpoint; got != nil {
		t.Fatalf("SNS BaseEndpoint = %q, want native AWS endpoint resolution", *got)
	}
	if got := clients.SQS.Options().BaseEndpoint; got != nil {
		t.Fatalf("SQS BaseEndpoint = %q, want native AWS endpoint resolution", *got)
	}
}

func TestNewUsesExplicitLocalEndpointAndCredentials(t *testing.T) {
	clients, err := New(context.Background(), " http://localstack:4566 ", "us-east-1", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := clients.S3.Options().BaseEndpoint; got == nil || *got != "http://localstack:4566" {
		t.Fatalf("S3 BaseEndpoint = %v", got)
	}
	if !clients.S3.Options().UsePathStyle {
		t.Fatal("LocalStack S3 client must use path-style addressing")
	}
}

func TestNewRejectsPartialStaticCredentials(t *testing.T) {
	if _, err := New(context.Background(), "", "us-east-1", "only-access-key", ""); err == nil {
		t.Fatal("expected partial static credentials to be rejected")
	}
}
