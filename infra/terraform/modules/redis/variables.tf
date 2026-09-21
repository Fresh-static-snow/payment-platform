variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_security_group_ids" { type = set(string) }

variable "node_type" {
  type    = string
  default = "cache.r7g.large"
}

variable "engine_version" {
  type    = string
  default = "7.1"
}

variable "num_cache_clusters" {
  type        = number
  description = "Total primary plus replica nodes."
  default     = 2
}

variable "snapshot_retention_days" {
  type    = number
  default = 7
}

variable "kms_key_arn" {
  type    = string
  default = null
}

variable "tags" {
  type    = map(string)
  default = {}
}
