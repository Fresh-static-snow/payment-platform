variable "aws_region" {
  type    = string
  default = "eu-central-1"
}

variable "aws_account_id" {
  type        = string
  description = "Optional safety rail: fail if credentials target another AWS account."
  default     = null
}

variable "cluster_admin_arns" {
  type        = set(string)
  description = "IAM principals granted EKS cluster admin access."
  default     = []

  validation {
    condition     = length(var.cluster_admin_arns) > 0
    error_message = "At least one explicit dev cluster administrator is required to avoid creating an inaccessible cluster."
  }
}

variable "endpoint_public_access" {
  type    = bool
  default = false
}

variable "public_access_cidrs" {
  type        = list(string)
  description = "Required allowlist when endpoint_public_access=true; never use 0.0.0.0/0."
  default     = []

  validation {
    condition     = alltrue([for cidr in var.public_access_cidrs : cidr != "0.0.0.0/0" && can(cidrhost(cidr, 0))])
    error_message = "public_access_cidrs must contain valid, restricted CIDRs."
  }
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
  type    = set(string)
  default = []
}

variable "external_application_secret_kms_key_arns" {
  type        = set(string)
  description = "Customer-managed KMS keys used by external application secrets."
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
