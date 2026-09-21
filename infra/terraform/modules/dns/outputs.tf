output "hosted_zone_id" { value = local.zone_id }
output "hosted_zone_arn" { value = local.zone_arn }
output "certificate_arn" { value = try(aws_acm_certificate_validation.this[0].certificate_arn, null) }
output "domain_name" { value = var.domain_name }
