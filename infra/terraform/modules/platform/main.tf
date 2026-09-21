locals {
  tags = merge(var.tags, {
    Project     = "payment-platform"
    Environment = var.environment
    ManagedBy   = "terraform"
  })

  repositories = toset([
    "payment-api",
    "payment-worker",
    "risk-service",
    "ledger-service",
    "receipt-service",
    "notification-service",
    "workflow-service",
    "payment-search-indexer",
    "payment-search-service",
    "analytics-ingestor",
    "outbox-relay",
    "migrations",
  ])
}

module "network" {
  source = "../network"

  name               = var.name
  vpc_cidr           = var.vpc_cidr
  az_count           = var.az_count
  enable_nat_gateway = true
  single_nat_gateway = var.single_nat_gateway
  enable_flow_logs   = true
  tags               = local.tags
}

module "ecr" {
  source = "../ecr"

  name         = var.name
  repositories = local.repositories
  tags         = local.tags
}

module "eks" {
  source = "../eks"

  cluster_name           = var.name
  kubernetes_version     = var.kubernetes_version
  subnet_ids             = module.network.private_subnet_ids
  endpoint_public_access = var.endpoint_public_access
  public_access_cidrs    = var.public_access_cidrs
  cluster_admin_arns     = var.cluster_admin_arns
  node_groups            = var.node_groups
  tags                   = local.tags
}

module "postgres" {
  source = "../postgres"

  name                       = "${var.name}-postgres"
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.data_subnet_ids
  allowed_security_group_ids = toset([module.eks.cluster_security_group_id])
  instance_class             = var.postgres_instance_class
  multi_az                   = var.postgres_multi_az
  deletion_protection        = var.postgres_deletion_protection
  skip_final_snapshot        = var.postgres_skip_final_snapshot
  tags                       = local.tags
}

module "redis" {
  source = "../redis"

  name                       = "${var.name}-redis"
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.data_subnet_ids
  allowed_security_group_ids = toset([module.eks.cluster_security_group_id])
  node_type                  = var.redis_node_type
  num_cache_clusters         = var.redis_num_cache_clusters
  tags                       = local.tags
}

module "msk" {
  source = "../msk"

  name                       = "${var.name}-kafka"
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.data_subnet_ids
  allowed_security_group_ids = toset([module.eks.cluster_security_group_id])
  broker_instance_type       = var.msk_broker_instance_type
  broker_volume_size_gib     = var.msk_broker_volume_size_gib
  authentication_mode        = var.msk_authentication_mode
  tags                       = local.tags
}

module "opensearch" {
  source = "../opensearch"

  name                       = "${var.name}-search"
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.data_subnet_ids
  allowed_security_group_ids = toset([module.eks.cluster_security_group_id])
  instance_type              = var.opensearch_instance_type
  instance_count             = var.opensearch_instance_count
  dedicated_master_enabled   = var.opensearch_dedicated_master_enabled
  tags                       = local.tags
}

module "messaging" {
  source = "../messaging"

  name          = var.name
  force_destroy = var.force_destroy_data
  tags          = local.tags
}

module "dns" {
  source = "../dns"

  domain_name        = var.domain_name
  create_hosted_zone = var.create_hosted_zone
  hosted_zone_id     = var.hosted_zone_id
  tags               = local.tags
}

module "iam" {
  source = "../iam"

  name                    = var.name
  oidc_provider_arn       = module.eks.oidc_provider_arn
  oidc_provider_url       = module.eks.oidc_provider_url
  receipt_bucket_arn      = module.messaging.receipt_bucket_arn
  notification_topic_arn  = module.messaging.notification_topic_arn
  notification_queue_arns = toset([module.messaging.notification_queue_arn, module.messaging.notification_dlq_arn])
  application_secret_arns = setunion(
    toset([
      module.postgres.master_user_secret_arn,
      module.redis.auth_secret_arn,
      module.opensearch.master_secret_arn,
    ]),
    var.external_application_secret_arns,
  )
  receipt_kms_key_arns      = toset([module.messaging.kms_key_arn])
  notification_kms_key_arns = toset([module.messaging.kms_key_arn])
  secret_kms_key_arns = setunion(toset([
    module.postgres.kms_key_arn,
    module.redis.kms_key_arn,
    module.opensearch.kms_key_arn,
  ]), var.external_application_secret_kms_key_arns)
  hosted_zone_arns = module.dns.hosted_zone_arn == null ? toset([]) : toset([module.dns.hosted_zone_arn])
  tags             = local.tags
}
