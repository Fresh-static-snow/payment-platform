#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)"
environment="${1:-dev}"
terraform_dir="$repo_root/infra/terraform/environments/$environment"
template_dir="$repo_root/deployments/kubernetes/overlays/aws/templates"
output_dir="$repo_root/deployments/kubernetes/overlays/aws/generated"
terraform_image="${TERRAFORM_IMAGE:-hashicorp/terraform:1.13.5}"

terraform_output() {
  output_name="$1"
  set -- docker run --rm \
    -e AWS_ACCESS_KEY_ID \
    -e AWS_SECRET_ACCESS_KEY \
    -e AWS_SESSION_TOKEN \
    -e AWS_PROFILE \
    -e AWS_DEFAULT_PROFILE \
    -e AWS_REGION \
    -e AWS_DEFAULT_REGION \
    -e AWS_ROLE_ARN \
    -e AWS_WEB_IDENTITY_TOKEN_FILE \
    -e AWS_EC2_METADATA_DISABLED \
    -e AWS_SDK_LOAD_CONFIG=1

  aws_config_dir="${AWS_CONFIG_DIR:-${HOME:-}/.aws}"
  if [ -n "$aws_config_dir" ] && [ -d "$aws_config_dir" ]; then
    set -- "$@" \
      -v "$aws_config_dir:/root/.aws:ro" \
      -e AWS_CONFIG_FILE=/root/.aws/config \
      -e AWS_SHARED_CREDENTIALS_FILE=/root/.aws/credentials
  fi

  "$@" \
    -v "$repo_root:/workspace" \
    -w "/workspace/infra/terraform/environments/$environment" \
    "$terraform_image" output -json "$output_name"
}

if [ -n "${PLATFORM_OUTPUT_JSON:-}" ]; then
  platform_json="$(jq -e . "$PLATFORM_OUTPUT_JSON")"
else
  platform_json="$(terraform_output platform)"
fi

if [ -n "${SECRET_OUTPUT_JSON:-}" ]; then
  secret_json="$(jq -e . "$SECRET_OUTPUT_JSON")"
else
  secret_json="$(terraform_output secret_arns)"
fi

json_value() {
  printf '%s' "$platform_json" | jq -er "$1"
}

secret_value() {
  printf '%s' "$secret_json" | jq -er "$1"
}

json_optional() {
  printf '%s' "$platform_json" | jq -r "$1 // empty"
}

export AWS_REGION="$(json_value '.region')"
export VPC_CIDR="$(json_value '.vpc_cidr')"
export POSTGRES_ENDPOINT="$(json_value '.postgres_endpoint')"
export POSTGRES_PORT="$(json_value '.postgres_port')"
export POSTGRES_DATABASE="$(json_value '.postgres_database')"
export REDIS_ENDPOINT="$(json_value '.redis_endpoint')"
export REDIS_PORT="$(json_value '.redis_port')"
export KAFKA_BROKERS="$(json_value '.kafka_bootstrap_brokers')"
KAFKA_AUTHENTICATION_MODE="$(json_value '.kafka_authentication_mode')"
export KAFKA_REPLICATION_FACTOR="$(json_value '.kafka_replication_factor')"
export OPENSEARCH_ENDPOINT="$(json_value '.opensearch_endpoint')"
export S3_BUCKET="$(json_value '.receipt_bucket_name')"
export SNS_TOPIC_ARN="$(json_value '.notification_topic_arn')"
export SQS_QUEUE_URL="$(json_value '.notification_queue_url')"
export CERTIFICATE_ARN="$(json_optional '.certificate_arn')"
export DOMAIN_NAME="$(json_optional '.domain_name')"
export RECEIPT_ROLE_ARN="$(json_value '.receipt_role_arn')"
export NOTIFICATION_ROLE_ARN="$(json_value '.notification_role_arn')"
export DOLLAR='$'

export POSTGRES_SECRET_ARN="$(secret_value '.postgres')"
export REDIS_SECRET_ARN="$(secret_value '.redis')"
export OPENSEARCH_SECRET_ARN="$(secret_value '.opensearch')"

export ECR_PAYMENT_API="$(json_value '.ecr_repository_urls["payment-api"]')"
export ECR_PAYMENT_WORKER="$(json_value '.ecr_repository_urls["payment-worker"]')"
export ECR_RISK_SERVICE="$(json_value '.ecr_repository_urls["risk-service"]')"
export ECR_LEDGER_SERVICE="$(json_value '.ecr_repository_urls["ledger-service"]')"
export ECR_RECEIPT_SERVICE="$(json_value '.ecr_repository_urls["receipt-service"]')"
export ECR_NOTIFICATION_SERVICE="$(json_value '.ecr_repository_urls["notification-service"]')"
export ECR_WORKFLOW_SERVICE="$(json_value '.ecr_repository_urls["workflow-service"]')"
export ECR_SEARCH_INDEXER="$(json_value '.ecr_repository_urls["payment-search-indexer"]')"
export ECR_SEARCH_SERVICE="$(json_value '.ecr_repository_urls["payment-search-service"]')"
export ECR_ANALYTICS_INGESTOR="$(json_value '.ecr_repository_urls["analytics-ingestor"]')"
export ECR_MIGRATIONS="$(json_value '.ecr_repository_urls["migrations"]')"

: "${IMAGE_TAG:?IMAGE_TAG must be an immutable release tag such as sha-<git-sha>}"
: "${CLICKHOUSE_URL:?CLICKHOUSE_URL is required}"
: "${CLICKHOUSE_USERNAME:?CLICKHOUSE_USERNAME is required}"
: "${CLICKHOUSE_SECRET_ARN:?CLICKHOUSE_SECRET_ARN is required}"
: "${TEMPORAL_ADDRESS:?TEMPORAL_ADDRESS is required}"
: "${AUTH_ISSUER_URL:?AUTH_ISSUER_URL is required}"
: "${AUTH_JWKS_URL:?AUTH_JWKS_URL is required}"
: "${OTEL_EXPORTER_OTLP_ENDPOINT:?OTEL_EXPORTER_OTLP_ENDPOINT is required}"
: "${CORS_ALLOWED_ORIGIN:?CORS_ALLOWED_ORIGIN is required}"

if [ "$KAFKA_AUTHENTICATION_MODE" != "tls" ]; then
  echo "AWS overlay currently supports msk_authentication_mode=tls only; add MSK IAM SASL support to the application clients before rendering IAM mode." >&2
  exit 1
fi

if { [ -n "$CERTIFICATE_ARN" ] && [ -z "$DOMAIN_NAME" ]; } || { [ -z "$CERTIFICATE_ARN" ] && [ -n "$DOMAIN_NAME" ]; }; then
  echo "certificate_arn and domain_name must either both be present or both be absent." >&2
  exit 1
fi

if [ -n "$DOMAIN_NAME" ]; then
  export INGRESS_RESOURCE="- ingress.yaml"
else
  export INGRESS_RESOURCE=""
fi

case "$output_dir" in
  "$repo_root"/deployments/kubernetes/overlays/aws/generated) ;;
  *) echo "refusing to replace unexpected output directory: $output_dir" >&2; exit 1 ;;
esac
rm -rf "$output_dir"
mkdir -p "$output_dir"

for template in "$template_dir"/*.tmpl; do
  if [ "$(basename "$template")" = "ingress.yaml.tmpl" ] && [ -z "$DOMAIN_NAME" ]; then
    continue
  fi
  target="$output_dir/$(basename "$template" .tmpl)"
  envsubst < "$template" > "$target"
done

kubectl kustomize "$output_dir" >/dev/null
printf 'Rendered AWS overlay: %s\n' "$output_dir"
