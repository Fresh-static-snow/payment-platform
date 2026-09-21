variable "aws_region" {
  type    = string
  default = "eu-central-1"
}

variable "bucket_name" {
  type        = string
  description = "Globally unique state bucket name. A deterministic account/region suffix is used when null."
  default     = null
}

variable "force_destroy" {
  type        = bool
  description = "Allow deletion of the state bucket and all versions. Keep false."
  default     = false
}

variable "tags" {
  type    = map(string)
  default = {}
}
