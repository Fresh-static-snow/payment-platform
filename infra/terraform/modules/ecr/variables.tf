variable "name" {
  description = "Platform name used in repository paths."
  type        = string
}

variable "repositories" {
  description = "Application image repository suffixes."
  type        = set(string)
}

variable "kms_key_arn" {
  description = "Optional KMS key ARN. AES256 is used when omitted."
  type        = string
  default     = null
}

variable "keep_tagged_images" {
  description = "Maximum number of tagged release images retained per repository."
  type        = number
  default     = 50
}

variable "untagged_expiry_days" {
  description = "Age after which untagged images are deleted."
  type        = number
  default     = 7
}

variable "tags" {
  type    = map(string)
  default = {}
}
