variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_security_group_ids" { type = set(string) }

variable "database_name" {
  type    = string
  default = "payments"
}

variable "master_username" {
  type    = string
  default = "payment_admin"
}

variable "engine_version" {
  type        = string
  description = "RDS PostgreSQL engine version. Pin this per environment after checking regional availability."
  default     = "17.11"
}

variable "instance_class" {
  type    = string
  default = "db.r7g.large"
}

variable "allocated_storage" {
  type    = number
  default = 100
}

variable "max_allocated_storage" {
  type    = number
  default = 1000
}

variable "multi_az" {
  type    = bool
  default = true
}

variable "backup_retention_days" {
  type    = number
  default = 14
}

variable "deletion_protection" {
  type    = bool
  default = true
}

variable "skip_final_snapshot" {
  type    = bool
  default = false
}

variable "kms_key_arn" {
  type    = string
  default = null
}

variable "tags" {
  type    = map(string)
  default = {}
}
