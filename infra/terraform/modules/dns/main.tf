locals {
  enabled  = var.domain_name != null && trimspace(var.domain_name) != ""
  zone_id  = var.create_hosted_zone ? try(aws_route53_zone.this[0].zone_id, null) : var.hosted_zone_id
  zone_arn = local.zone_id == null ? null : "arn:aws:route53:::hostedzone/${local.zone_id}"
}

resource "terraform_data" "validation" {
  input = local.enabled

  lifecycle {
    precondition {
      condition     = !local.enabled || var.create_hosted_zone || var.hosted_zone_id != null
      error_message = "hosted_zone_id is required when domain_name is set and create_hosted_zone is false."
    }
  }
}

resource "aws_route53_zone" "this" {
  count = local.enabled && var.create_hosted_zone ? 1 : 0

  name = var.domain_name
  tags = merge(var.tags, { Component = "dns" })
}

resource "aws_acm_certificate" "this" {
  count = local.enabled ? 1 : 0

  domain_name               = var.domain_name
  subject_alternative_names = setunion(var.subject_alternative_names, ["*.${var.domain_name}"])
  validation_method         = "DNS"
  tags                      = merge(var.tags, { Component = "tls" })

  lifecycle {
    create_before_destroy = true
  }

  depends_on = [terraform_data.validation]
}

resource "aws_route53_record" "validation" {
  for_each = local.enabled ? {
    for option in aws_acm_certificate.this[0].domain_validation_options :
    option.domain_name => {
      name   = option.resource_record_name
      record = option.resource_record_value
      type   = option.resource_record_type
    }
  } : {}

  allow_overwrite = true
  zone_id         = local.zone_id
  name            = each.value.name
  type            = each.value.type
  ttl             = 60
  records         = [each.value.record]
}

resource "aws_acm_certificate_validation" "this" {
  count = local.enabled ? 1 : 0

  certificate_arn         = aws_acm_certificate.this[0].arn
  validation_record_fqdns = [for record in aws_route53_record.validation : record.fqdn]
}
