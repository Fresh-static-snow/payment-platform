# AWS infrastructure with Terraform

This stack provisions the managed infrastructure for the payment platform. It deliberately owns infrastructure only; application rollout remains in Kustomize/GitOps.

## What is provisioned

- a multi-AZ VPC with public, private, and isolated data subnets, NAT gateways, VPC flow logs, S3 and interface endpoints;
- encrypted ECR repositories with immutable tags, scan-on-push, and lifecycle policies;
- an encrypted EKS cluster, managed node groups, control-plane logs, API access entries, add-ons, and an OIDC provider for IRSA;
- RDS PostgreSQL 17 with AWS-managed master credentials, backups/PITR controls, deletion protection, Performance Insights, and logical replication parameters for Debezium;
- ElastiCache Redis with TLS, replication/failover, snapshots, and credentials in Secrets Manager;
- Amazon MSK with TLS, broker logs, Prometheus exporters, encrypted EBS, plus idempotent application/DLQ/Connect topic creation from EKS;
- VPC OpenSearch with TLS, encryption, fine-grained access control, audit/application logs, and credentials in Secrets Manager;
- an encrypted/versioned receipt bucket, SNS topic, SQS queue and DLQ with redrive and least-privilege queue policy;
- workload IRSA roles for receipt, notification, External Secrets, AWS Load Balancer Controller, and ExternalDNS;
- optional Route53/ACM DNS and certificate validation;
- a separate encrypted/versioned S3 backend using native Terraform S3 lock files.

ClickHouse, Temporal, and Keycloak are intentionally external contracts. AWS has no native managed ClickHouse or Temporal equivalent, and identity topology is organization-specific. Supply their private endpoints and secret ARN to the rendered Kubernetes overlay or manage them in separate provider-specific stacks.

## Layout

```text
infra/terraform/
├── bootstrap/             # one-time remote state bucket and KMS key
├── environments/
│   ├── dev/
│   └── prod/
├── modules/               # network, EKS, ECR, data, messaging, IAM, DNS
├── scripts/               # Terraform output -> Kustomize overlay
└── testdata/              # non-secret render fixture
```

## Safety and cost

This stack creates paid resources: NAT gateways, EKS, RDS, MSK, ElastiCache, and OpenSearch. Review the plan and AWS pricing before applying. The project never runs `terraform apply` automatically.

Production defaults enable Multi-AZ data services, three NAT gateways, deletion protection, backups, and final snapshots. The dev environment is cheaper but still not free. Use a dedicated AWS account and the `aws_account_id` safety rail.

Never commit `terraform.tfvars`, `backend.hcl`, state, or plan files. Provider credentials come from the normal AWS credential chain, SSO, or an assumed CI role—not Terraform variables. Make forwards standard AWS environment credentials into the Terraform container and mounts `${AWS_CONFIG_DIR:-$HOME/.aws}` read-only so profiles and SSO caches remain available; `AWS_DOCKER_EXTRA_ARGS` can add a CI-specific token mount when needed.

## 1. Bootstrap remote state

The bootstrap state starts locally because it creates the backend itself:

```bash
cp infra/terraform/bootstrap/terraform.tfvars.example \
  infra/terraform/bootstrap/terraform.tfvars
make terraform-bootstrap-init
make terraform-bootstrap-validate
make terraform-bootstrap-plan

# Review the plan, then apply explicitly:
make terraform-bootstrap-apply
```

Copy `terraform output -raw backend_hcl` to each environment's `backend.hcl`, add the environment-specific `key` shown in `backend.hcl.example`, and securely retain the small bootstrap state. The backend uses S3 versioning, KMS encryption, TLS-only access, and `use_lockfile = true`; DynamoDB locking is not required.

## 2. Plan an environment

```bash
cp infra/terraform/environments/dev/backend.hcl.example \
  infra/terraform/environments/dev/backend.hcl
cp infra/terraform/environments/dev/terraform.tfvars.example \
  infra/terraform/environments/dev/terraform.tfvars

make terraform-fmt-check
make terraform-init TF_ENV=dev
make terraform-validate TF_ENV=dev
make terraform-plan TF_ENV=dev

# Review platform.tfplan, then apply explicitly:
make terraform-apply TF_ENV=dev
```

Inspect the saved plan before explicitly applying it. Repeat with `TF_ENV=prod`; production variables require an account ID, an administrator role, a DNS name, and an existing hosted zone.

`terraform-validate` keeps its working directory ephemeral and shares downloaded providers through the `payment-platform-terraform-plugin-cache` Docker volume. This prevents a separate large `.terraform` directory from being left in every environment. The cache contains only re-downloadable provider binaries and can be removed with `docker volume rm payment-platform-terraform-plugin-cache` when it is no longer needed.

MSK defaults to encrypted TLS transport without SASL because the current Go services and Debezium support standard TLS directly. The module also supports `msk_authentication_mode = "iam"`, but the renderer deliberately rejects that mode until AWS MSK IAM SASL is added to every Kafka client and Debezium.

## 3. Platform controllers

Install these controllers through your GitOps/Helm layer before applying application manifests:

- External Secrets Operator, service account `external-secrets/external-secrets` annotated with `external_secrets_role_arn`;
- AWS Load Balancer Controller, service account `kube-system/aws-load-balancer-controller` annotated with `load_balancer_controller_role_arn`;
- ExternalDNS when DNS is enabled, service account `external-dns/external-dns` annotated with `external_dns_role_arn`;
- Stakater Reloader, with a tested chart version pinned and the `annotations` reload strategy, so rotated External Secrets trigger controlled workload rollouts without persistent GitOps drift.

For Argo CD, pair that Reloader strategy with an application-level ignore rule and make sync respect it:

```yaml
spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jsonPointers:
        - /spec/template/metadata/annotations/reloader.stakater.com~1last-reloaded-from
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true
```

The vendored [AWS Load Balancer Controller IAM policy](https://github.com/kubernetes-sigs/aws-load-balancer-controller/blob/v3.5.0/docs/install/iam_policy.json) is pinned to controller v3.5.0 and adapted to the active AWS partition. Upgrade the controller and `modules/iam/policies/aws-load-balancer-controller-v3.5.0.json` together; the upstream policy is versioned with controller releases.

The Terraform output named `platform` is the non-secret deployment contract. `secret_arns` identifies RDS, Redis, and OpenSearch Secrets Manager entries. Receipt and notification pods use dedicated IRSA service accounts; static AWS keys are not rendered.

## 4. Render the AWS Kustomize overlay

Build and push every ECR image under the same immutable release tag. Then export the provider-specific services:

```bash
export IMAGE_TAG='sha-0123456789ab' # immutable CI commit tag
export CLICKHOUSE_URL='https://clickhouse.example.internal'
export CLICKHOUSE_USERNAME='analytics'
export CLICKHOUSE_SECRET_ARN='arn:aws:secretsmanager:eu-central-1:123456789012:secret:clickhouse'
export TEMPORAL_ADDRESS='temporal.example.internal:7233'
export AUTH_ISSUER_URL='https://auth.example.com/realms/payments'
export AUTH_JWKS_URL="$AUTH_ISSUER_URL/protocol/openid-connect/certs"
export OTEL_EXPORTER_OTLP_ENDPOINT='otel-collector.observability.svc:4317'
export CORS_ALLOWED_ORIGIN='https://app.payments.example.com'

make k8s-aws-validate TF_ENV=dev
kubectl apply -k deployments/kubernetes/overlays/aws/generated
```

Add `CLICKHOUSE_SECRET_ARN` (and any other provider-managed secrets) to `external_application_secret_arns` before planning, otherwise the External Secrets role cannot read it. If an external secret uses a customer-managed KMS key, also add that key to `external_application_secret_kms_key_arns`; its key policy must permit this account/role, while AWS-managed Secrets Manager keys need no entry. Receipt and notification roles can use only the messaging KMS key, while External Secrets can decrypt only the database/cache/search keys plus this explicit external set. The renderer reads Terraform outputs, checks all required external values, replaces ECR repositories/IRSA roles/endpoints, and validates the resulting overlay before returning. When dev has no Route53/ACM output it omits the public Ingress; services remain reachable only inside the cluster. The rendered directory is environment-specific and ignored by Git.

External Secrets creates four least-exposure Kubernetes secrets: `payment-database-secret`, `payment-redis-secret`, `payment-search-secret`, and `payment-analytics-secret`. Each workload receives only the credential group it uses; the database URL template URL-encodes the RDS password. Native AWS SDK endpoints and the EKS web-identity credential chain are used when `AWS_ENDPOINT` and static keys are absent.

For an initial Argo CD deployment, sync waves create the namespace first, then the cluster store and external secrets, then run Kafka topic initialization and database migration as blocking Sync hooks before application workloads. Reloader annotations name the exact secrets consumed by each Deployment; this is required because Kubernetes does not update environment variables in already-running containers.

## Operational notes

- The AWS overlay creates application, DLQ, heartbeat, and compacted Kafka Connect topics, runs Debezium over MSK TLS, and idempotently creates/updates the outbox connector. Monitor connector health and replication-slot lag, and retain a recovery runbook.
- Data subnets have no default internet route. EKS workloads use private AWS endpoints where configured; the egress NetworkPolicy restricts RDS, Redis, MSK, and OpenSearch ports to the platform VPC, permits only the declared HTTPS/observability/provider ports outside it, and explicitly blocks instance metadata. Tighten the external destination CIDRs further or use CNI-aware policies when provider CIDRs are stable.
- OpenSearch's resource policy is broad only at the IAM resource-policy layer; the endpoint is VPC-only, security-group restricted, HTTPS-only, and additionally protected by fine-grained credentials.
- RDS-managed master credentials rotate every seven days by default. The database ExternalSecret refreshes every minute and Reloader restarts only database consumers; Redis, OpenSearch, and external credentials refresh every five minutes and restart only their consumers. Monitor reconciliation and rollout failures because a provider-side rotation can still create a short propagation window.
- Before destroy, export backups and disable deletion protection deliberately. The state bucket has `prevent_destroy = true` by design.
