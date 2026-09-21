variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_security_group_ids" { type = set(string) }

variable "engine_version" {
  type    = string
  default = "OpenSearch_2.19"
}

variable "instance_type" {
  type    = string
  default = "r7g.large.search"
}

variable "instance_count" {
  type    = number
  default = 3
}

variable "dedicated_master_enabled" {
  type    = bool
  default = true
}

variable "dedicated_master_type" {
  type    = string
  default = "m7g.large.search"
}

variable "ebs_volume_size" {
  type    = number
  default = 100
}

variable "kms_key_arn" {
  type    = string
  default = null
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "tags" {
  type    = map(string)
  default = {}
}
