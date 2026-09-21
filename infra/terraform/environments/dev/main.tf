module "platform" {
  source = "../../modules/platform"

  name        = "payment-platform-dev"
  environment = "dev"
  vpc_cidr    = "10.40.0.0/16"
  az_count    = 2

  single_nat_gateway     = true
  kubernetes_version     = "1.32"
  cluster_admin_arns     = var.cluster_admin_arns
  endpoint_public_access = var.endpoint_public_access
  public_access_cidrs    = var.public_access_cidrs
  node_groups = {
    general = {
      instance_types = ["m7i.large"]
      capacity_type  = "SPOT"
      min_size       = 1
      max_size       = 4
      desired_size   = 2
      disk_size      = 60
      labels         = { workload = "general" }
    }
  }

  postgres_instance_class      = "db.t4g.medium"
  postgres_multi_az            = false
  postgres_deletion_protection = false
  postgres_skip_final_snapshot = true

  redis_node_type          = "cache.t4g.small"
  redis_num_cache_clusters = 2

  msk_broker_instance_type   = "kafka.m7g.large"
  msk_broker_volume_size_gib = 100
  msk_authentication_mode    = "tls"

  opensearch_instance_type            = "m7g.large.search"
  opensearch_instance_count           = 2
  opensearch_dedicated_master_enabled = false

  force_destroy_data                       = true
  domain_name                              = var.domain_name
  create_hosted_zone                       = var.create_hosted_zone
  hosted_zone_id                           = var.hosted_zone_id
  external_application_secret_arns         = var.external_application_secret_arns
  external_application_secret_kms_key_arns = var.external_application_secret_kms_key_arns
  tags                                     = var.tags
}
