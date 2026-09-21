data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  common_tags = merge(var.tags, { Component = "search" })
  kms_key_arn = coalesce(var.kms_key_arn, try(aws_kms_key.this[0].arn, null))
  az_count    = min(3, length(var.subnet_ids))
  domain_arn  = "arn:aws:es:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:domain/${var.name}"
}

resource "aws_kms_key" "this" {
  count = var.kms_key_arn == null ? 1 : 0

  description             = "${var.name} OpenSearch and credential encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = local.common_tags
}

resource "aws_kms_alias" "this" {
  count         = var.kms_key_arn == null ? 1 : 0
  name          = "alias/${var.name}-opensearch"
  target_key_id = aws_kms_key.this[0].key_id
}

resource "random_password" "master" {
  length           = 48
  special          = true
  override_special = "!#$%&*+-=?^_~"
}

resource "aws_secretsmanager_secret" "master" {
  name                    = "${var.name}/opensearch/master"
  description             = "OpenSearch fine-grained access credentials for ${var.name}"
  kms_key_id              = local.kms_key_arn
  recovery_window_in_days = 30
  tags                    = local.common_tags
}

resource "aws_secretsmanager_secret_version" "master" {
  secret_id = aws_secretsmanager_secret.master.id
  secret_string = jsonencode({
    username = "search_admin"
    password = random_password.master.result
  })
}

resource "aws_security_group" "this" {
  name_prefix = "${var.name}-opensearch-"
  description = "OpenSearch HTTPS access from EKS workloads"
  vpc_id      = var.vpc_id

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.common_tags, { Name = "${var.name}-opensearch" })
}

resource "aws_vpc_security_group_ingress_rule" "client" {
  for_each = var.allowed_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443
  description                  = "OpenSearch HTTPS from ${each.value}"
}

resource "aws_cloudwatch_log_group" "application" {
  name              = "/aws/opensearch/${var.name}/application"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

resource "aws_cloudwatch_log_group" "audit" {
  name              = "/aws/opensearch/${var.name}/audit"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

data "aws_iam_policy_document" "logs" {
  statement {
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "${aws_cloudwatch_log_group.application.arn}:*",
      "${aws_cloudwatch_log_group.audit.arn}:*",
    ]
    principals {
      type        = "Service"
      identifiers = ["es.amazonaws.com"]
    }
  }
}

resource "aws_cloudwatch_log_resource_policy" "this" {
  policy_name     = "${var.name}-opensearch"
  policy_document = data.aws_iam_policy_document.logs.json
}

data "aws_iam_policy_document" "domain" {
  statement {
    actions   = ["es:ESHttp*"]
    resources = ["${local.domain_arn}/*"]
    principals {
      type        = "AWS"
      identifiers = ["*"]
    }
  }
}

resource "aws_opensearch_domain" "this" {
  domain_name    = var.name
  engine_version = var.engine_version

  cluster_config {
    instance_type            = var.instance_type
    instance_count           = var.instance_count
    zone_awareness_enabled   = local.az_count > 1
    dedicated_master_enabled = var.dedicated_master_enabled
    dedicated_master_type    = var.dedicated_master_enabled ? var.dedicated_master_type : null
    dedicated_master_count   = var.dedicated_master_enabled ? 3 : null

    dynamic "zone_awareness_config" {
      for_each = local.az_count > 1 ? [1] : []
      content {
        availability_zone_count = local.az_count
      }
    }
  }

  vpc_options {
    subnet_ids         = slice(var.subnet_ids, 0, local.az_count)
    security_group_ids = [aws_security_group.this.id]
  }

  ebs_options {
    ebs_enabled = true
    volume_type = "gp3"
    volume_size = var.ebs_volume_size
    iops        = 3000
    throughput  = 125
  }

  encrypt_at_rest {
    enabled    = true
    kms_key_id = local.kms_key_arn
  }

  node_to_node_encryption {
    enabled = true
  }

  domain_endpoint_options {
    enforce_https       = true
    tls_security_policy = "Policy-Min-TLS-1-2-2019-07"
  }

  advanced_security_options {
    enabled                        = true
    internal_user_database_enabled = true
    master_user_options {
      master_user_name     = "search_admin"
      master_user_password = random_password.master.result
    }
  }

  access_policies = data.aws_iam_policy_document.domain.json

  log_publishing_options {
    cloudwatch_log_group_arn = aws_cloudwatch_log_group.application.arn
    log_type                 = "ES_APPLICATION_LOGS"
    enabled                  = true
  }

  log_publishing_options {
    cloudwatch_log_group_arn = aws_cloudwatch_log_group.audit.arn
    log_type                 = "AUDIT_LOGS"
    enabled                  = true
  }

  tags = local.common_tags

  depends_on = [aws_cloudwatch_log_resource_policy.this]
}
