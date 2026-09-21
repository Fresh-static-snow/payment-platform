output "vpc_id" { value = module.network.vpc_id }
output "vpc_cidr" { value = module.network.vpc_cidr }
output "private_subnet_ids" { value = module.network.private_subnet_ids }
output "data_subnet_ids" { value = module.network.data_subnet_ids }

output "eks_cluster_name" { value = module.eks.cluster_name }
output "eks_cluster_endpoint" { value = module.eks.cluster_endpoint }
output "eks_oidc_provider_arn" { value = module.eks.oidc_provider_arn }

output "ecr_repository_urls" { value = module.ecr.repository_urls }

output "postgres_endpoint" { value = module.postgres.endpoint }
output "postgres_port" { value = module.postgres.port }
output "postgres_database" { value = module.postgres.database_name }
output "postgres_master_secret_arn" { value = module.postgres.master_user_secret_arn }

output "redis_endpoint" { value = module.redis.primary_endpoint }
output "redis_port" { value = module.redis.port }
output "redis_auth_secret_arn" { value = module.redis.auth_secret_arn }

output "kafka_bootstrap_brokers" { value = module.msk.bootstrap_brokers }
output "kafka_authentication_mode" { value = module.msk.authentication_mode }
output "kafka_replication_factor" { value = module.msk.replication_factor }

output "opensearch_endpoint" { value = module.opensearch.endpoint }
output "opensearch_master_secret_arn" { value = module.opensearch.master_secret_arn }

output "receipt_bucket_name" { value = module.messaging.receipt_bucket_name }
output "notification_topic_arn" { value = module.messaging.notification_topic_arn }
output "notification_queue_url" { value = module.messaging.notification_queue_url }
output "notification_dlq_url" { value = module.messaging.notification_dlq_url }

output "certificate_arn" { value = module.dns.certificate_arn }
output "hosted_zone_id" { value = module.dns.hosted_zone_id }
output "domain_name" { value = module.dns.domain_name }

output "receipt_role_arn" { value = module.iam.receipt_role_arn }
output "notification_role_arn" { value = module.iam.notification_role_arn }
output "external_secrets_role_arn" { value = module.iam.external_secrets_role_arn }
output "load_balancer_controller_role_arn" { value = module.iam.load_balancer_controller_role_arn }
output "external_dns_role_arn" { value = module.iam.external_dns_role_arn }
