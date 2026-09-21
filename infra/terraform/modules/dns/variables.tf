variable "domain_name" {
  type        = string
  description = "Base DNS name. Set null to skip Route53 and ACM."
  default     = null
}

variable "create_hosted_zone" {
  type    = bool
  default = false
}

variable "hosted_zone_id" {
  type        = string
  description = "Existing public hosted zone ID when create_hosted_zone is false."
  default     = null
}

variable "subject_alternative_names" {
  type        = set(string)
  description = "Additional names included in the ACM certificate."
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
