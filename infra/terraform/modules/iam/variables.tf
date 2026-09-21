variable "name" { type = string }
variable "oidc_provider_arn" { type = string }
variable "oidc_provider_url" { type = string }

variable "namespace" {
  type    = string
  default = "payment-platform"
}

variable "receipt_bucket_arn" { type = string }
variable "notification_topic_arn" { type = string }
variable "notification_queue_arns" { type = set(string) }
variable "application_secret_arns" { type = set(string) }
variable "receipt_kms_key_arns" { type = set(string) }
variable "notification_kms_key_arns" { type = set(string) }
variable "secret_kms_key_arns" { type = set(string) }

variable "hosted_zone_arns" {
  type    = set(string)
  default = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
