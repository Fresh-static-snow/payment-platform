output "state_bucket" {
  value       = aws_s3_bucket.state.id
  description = "Use this value as bucket in each environment backend.hcl."
}

output "state_kms_key_arn" {
  value       = aws_kms_key.state.arn
  description = "Use this value as kms_key_id in each environment backend.hcl."
}

output "backend_hcl" {
  description = "Non-sensitive partial backend configuration template."
  value       = <<-EOT
    bucket       = "${aws_s3_bucket.state.id}"
    region       = "${var.aws_region}"
    kms_key_id   = "${aws_kms_key.state.arn}"
    encrypt      = true
    use_lockfile = true
  EOT
}
