variable "name" { type = string }

variable "message_retention_seconds" {
  type    = number
  default = 345600
}

variable "max_receive_count" {
  type    = number
  default = 4
}

variable "receipt_retention_days" {
  type    = number
  default = 2555
}

variable "force_destroy" {
  type        = bool
  description = "Permit deletion of a non-empty receipt bucket. Keep false outside disposable dev environments."
  default     = false
}

variable "kms_key_arn" {
  type    = string
  default = null
}

variable "tags" {
  type    = map(string)
  default = {}
}
