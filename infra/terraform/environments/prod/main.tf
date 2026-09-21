module "platform" {
  source = "../../modules/platform"

  name        = "payment-platform-prod"
  environment = "prod"
  vpc_cidr    = "10.60.0.0/16"
  az_count    = 3

  single_nat_gateway     = false
  kubernetes_version     = "1.32"
  cluster_admin_arns     = var.cluster_admin_arns
  endpoint_public_access = false
  public_access_cidrs    = []
  node_groups = {
    system = {
      instance_types = ["m7i.large"]
      capacity_type  = "ON_DEMAND"
      min_size       = 3
      max_size       = 6
      desired_size   = 3
      disk_size      = 80
      labels         = { workload = "system" }
    }
    application = {
      instance_types = ["m7i.xlarge"]
      capacity_type  = "ON_DEMAND"
      min_size       = 3
      max_size       = 12
      desired_size   = 3
      disk_size      = 100
      labels         = { workload = "application" }
    }
  }

  postgres_instance_class      = "db.r7g.large"
  postgres_multi_az            = true
  postgres_deletion_protection = true
  postgres_skip_final_snapshot = false

  redis_node_type          = "cache.r7g.large"
  redis_num_cache_clusters = 3

  msk_broker_instance_type   = "kafka.m7g.large"
  msk_broker_volume_size_gib = 500
  msk_authentication_mode    = "tls"

  opensearch_instance_type            = "r7g.large.search"
  opensearch_instance_count           = 3
  opensearch_dedicated_master_enabled = true

  force_destroy_data                       = false
  domain_name                              = var.domain_name
  create_hosted_zone                       = false
  hosted_zone_id                           = var.hosted_zone_id
  external_application_secret_arns         = var.external_application_secret_arns
  external_application_secret_kms_key_arns = var.external_application_secret_kms_key_arns
  tags                                     = var.tags
}
