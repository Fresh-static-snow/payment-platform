variable "aws_region" {
  type    = string
  default = "eu-central-1"
}

variable "aws_account_id" {
  type        = string
  description = "Mandatory safety rail for production credentials."

  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "aws_account_id must contain exactly 12 digits."
  }
}

variable "cluster_admin_arns" {
  type        = set(string)
  description = "Explicit production administrator roles."

  validation {
    condition     = length(var.cluster_admin_arns) > 0
    error_message = "At least one explicit production cluster administrator is required."
  }
}

variable "domain_name" {
  type        = string
  description = "Production DNS zone used for ACM and ingress."
}

variable "hosted_zone_id" {
  type        = string
  description = "Existing production public Route53 hosted zone ID."
}

variable "external_application_secret_arns" {
  type        = set(string)
  description = "Provider-specific ClickHouse, Temporal, and Keycloak secrets."
  default     = []
}

variable "external_application_secret_kms_key_arns" {
  type        = set(string)
  description = "Customer-managed KMS keys used by provider-specific application secrets."
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
