variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_security_group_ids" { type = set(string) }

variable "kafka_version" {
  type        = string
  description = "MSK Kafka version available in the selected AWS region."
  default     = "3.8.x"
}

variable "broker_instance_type" {
  type    = string
  default = "kafka.m7g.large"
}

variable "broker_volume_size_gib" {
  type    = number
  default = 200
}

variable "brokers_per_az" {
  type    = number
  default = 1
}

variable "authentication_mode" {
  type        = string
  description = "tls uses encrypted unauthenticated connections inside the VPC; iam enables SASL/IAM."
  default     = "tls"

  validation {
    condition     = contains(["tls", "iam"], var.authentication_mode)
    error_message = "authentication_mode must be tls or iam."
  }
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
