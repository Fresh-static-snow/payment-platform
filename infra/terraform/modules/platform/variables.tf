variable "name" {
  type        = string
  description = "Short globally consistent platform/environment name."
}

variable "environment" {
  type        = string
  description = "Environment tag such as dev, staging, or prod."
}

variable "vpc_cidr" {
  type    = string
  default = "10.40.0.0/16"
}

variable "az_count" {
  type    = number
  default = 3
}

variable "single_nat_gateway" {
  type    = bool
  default = false
}

variable "kubernetes_version" {
  type    = string
  default = "1.32"
}

variable "cluster_admin_arns" {
  type    = set(string)
  default = []
}

variable "endpoint_public_access" {
  type    = bool
  default = false
}

variable "public_access_cidrs" {
  type    = list(string)
  default = []
}

variable "node_groups" {
  type = map(object({
    instance_types = list(string)
    capacity_type  = string
    min_size       = number
    max_size       = number
    desired_size   = number
    disk_size      = number
    labels         = map(string)
  }))
}

variable "postgres_instance_class" { type = string }
variable "postgres_multi_az" { type = bool }
variable "postgres_deletion_protection" { type = bool }
variable "postgres_skip_final_snapshot" { type = bool }

variable "redis_node_type" { type = string }
variable "redis_num_cache_clusters" { type = number }

variable "msk_broker_instance_type" { type = string }
variable "msk_broker_volume_size_gib" { type = number }
variable "msk_authentication_mode" {
  type    = string
  default = "tls"
}

variable "opensearch_instance_type" { type = string }
variable "opensearch_instance_count" { type = number }
variable "opensearch_dedicated_master_enabled" { type = bool }

variable "force_destroy_data" {
  type        = bool
  description = "Allow deletion of non-empty application buckets. Only true in disposable dev."
  default     = false
}

variable "domain_name" {
  type    = string
  default = null
}

variable "create_hosted_zone" {
  type    = bool
  default = false
}

variable "hosted_zone_id" {
  type    = string
  default = null
}

variable "external_application_secret_arns" {
  type        = set(string)
  description = "Secrets for externally managed Keycloak, Temporal, or ClickHouse credentials exposed through External Secrets."
  default     = []
}

variable "external_application_secret_kms_key_arns" {
  type        = set(string)
  description = "Customer-managed KMS keys used by external application secrets; leave empty for AWS-managed Secrets Manager keys."
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
