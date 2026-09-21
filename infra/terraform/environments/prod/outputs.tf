output "platform" {
  description = "Non-sensitive production deployment contract consumed by GitOps."
  value = {
    region                            = var.aws_region
    vpc_cidr                          = module.platform.vpc_cidr
    eks_cluster_name                  = module.platform.eks_cluster_name
    ecr_repository_urls               = module.platform.ecr_repository_urls
    postgres_endpoint                 = module.platform.postgres_endpoint
    postgres_port                     = module.platform.postgres_port
    postgres_database                 = module.platform.postgres_database
    redis_endpoint                    = module.platform.redis_endpoint
    redis_port                        = module.platform.redis_port
    kafka_bootstrap_brokers           = module.platform.kafka_bootstrap_brokers
    kafka_authentication_mode         = module.platform.kafka_authentication_mode
    kafka_replication_factor          = module.platform.kafka_replication_factor
    opensearch_endpoint               = module.platform.opensearch_endpoint
    receipt_bucket_name               = module.platform.receipt_bucket_name
    notification_topic_arn            = module.platform.notification_topic_arn
    notification_queue_url            = module.platform.notification_queue_url
    certificate_arn                   = module.platform.certificate_arn
    hosted_zone_id                    = module.platform.hosted_zone_id
    domain_name                       = module.platform.domain_name
    receipt_role_arn                  = module.platform.receipt_role_arn
    notification_role_arn             = module.platform.notification_role_arn
    external_secrets_role_arn         = module.platform.external_secrets_role_arn
    load_balancer_controller_role_arn = module.platform.load_balancer_controller_role_arn
    external_dns_role_arn             = module.platform.external_dns_role_arn
  }
}

output "secret_arns" {
  sensitive = true
  value = {
    postgres   = module.platform.postgres_master_secret_arn
    redis      = module.platform.redis_auth_secret_arn
    opensearch = module.platform.opensearch_master_secret_arn
  }
}
