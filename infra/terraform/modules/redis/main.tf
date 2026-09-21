locals {
  common_tags = merge(var.tags, { Component = "redis" })
  kms_key_arn = coalesce(var.kms_key_arn, try(aws_kms_key.this[0].arn, null))
}

resource "aws_kms_key" "this" {
  count = var.kms_key_arn == null ? 1 : 0

  description             = "${var.name} ElastiCache and credential encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = local.common_tags
}

resource "aws_kms_alias" "this" {
  count         = var.kms_key_arn == null ? 1 : 0
  name          = "alias/${var.name}-redis"
  target_key_id = aws_kms_key.this[0].key_id
}

resource "random_password" "auth" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "auth" {
  name                    = "${var.name}/redis/auth"
  description             = "TLS Redis ACL credentials for ${var.name}"
  kms_key_id              = local.kms_key_arn
  recovery_window_in_days = 30
  tags                    = local.common_tags
}

resource "aws_secretsmanager_secret_version" "auth" {
  secret_id = aws_secretsmanager_secret.auth.id
  secret_string = jsonencode({
    username = "default"
    password = random_password.auth.result
  })
}

resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = local.common_tags
}

resource "aws_security_group" "this" {
  name_prefix = "${var.name}-redis-"
  description = "Redis access from EKS workloads"
  vpc_id      = var.vpc_id

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.common_tags, { Name = "${var.name}-redis" })
}

resource "aws_vpc_security_group_ingress_rule" "client" {
  for_each = var.allowed_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "Redis TLS from ${each.value}"
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = var.name
  description          = "Payment platform Redis"

  engine               = "redis"
  engine_version       = var.engine_version
  node_type            = var.node_type
  port                 = 6379
  num_cache_clusters   = var.num_cache_clusters
  parameter_group_name = "default.redis7"

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.this.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  kms_key_id                 = local.kms_key_arn
  auth_token                 = random_password.auth.result
  auth_token_update_strategy = "ROTATE"

  automatic_failover_enabled = var.num_cache_clusters > 1
  multi_az_enabled           = var.num_cache_clusters > 1
  auto_minor_version_upgrade = true
  apply_immediately          = false

  snapshot_retention_limit = var.snapshot_retention_days
  snapshot_window          = "01:00-02:00"
  maintenance_window       = "sun:02:30-sun:03:30"

  tags = local.common_tags
}
