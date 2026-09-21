locals {
  common_tags         = merge(var.tags, { Component = "kafka" })
  kms_key_arn         = coalesce(var.kms_key_arn, try(aws_kms_key.this[0].arn, null))
  replication_factor  = min(3, length(var.subnet_ids))
  min_insync_replicas = max(1, local.replication_factor - 1)
  client_port         = var.authentication_mode == "iam" ? 9098 : 9094
}

resource "aws_kms_key" "this" {
  count = var.kms_key_arn == null ? 1 : 0

  description             = "${var.name} MSK encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = local.common_tags
}

resource "aws_kms_alias" "this" {
  count         = var.kms_key_arn == null ? 1 : 0
  name          = "alias/${var.name}-msk"
  target_key_id = aws_kms_key.this[0].key_id
}

resource "aws_security_group" "this" {
  name_prefix = "${var.name}-msk-"
  description = "MSK access from EKS workloads"
  vpc_id      = var.vpc_id

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.common_tags, { Name = "${var.name}-msk" })
}

resource "aws_vpc_security_group_ingress_rule" "client" {
  for_each = var.allowed_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = local.client_port
  to_port                      = local.client_port
  description                  = "Kafka from ${each.value}"
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/aws/msk/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

resource "aws_msk_configuration" "this" {
  name              = var.name
  kafka_versions    = [var.kafka_version]
  server_properties = <<-PROPERTIES
    auto.create.topics.enable=false
    default.replication.factor=${local.replication_factor}
    min.insync.replicas=${local.min_insync_replicas}
    num.partitions=3
    unclean.leader.election.enable=false
    log.retention.hours=168
  PROPERTIES
}

resource "aws_msk_cluster" "this" {
  cluster_name           = var.name
  kafka_version          = var.kafka_version
  number_of_broker_nodes = length(var.subnet_ids) * var.brokers_per_az

  broker_node_group_info {
    instance_type   = var.broker_instance_type
    client_subnets  = var.subnet_ids
    security_groups = [aws_security_group.this.id]

    storage_info {
      ebs_storage_info {
        volume_size = var.broker_volume_size_gib
      }
    }
  }

  configuration_info {
    arn      = aws_msk_configuration.this.arn
    revision = aws_msk_configuration.this.latest_revision
  }

  encryption_info {
    encryption_at_rest_kms_key_arn = local.kms_key_arn
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
  }

  client_authentication {
    unauthenticated = var.authentication_mode == "tls"

    dynamic "sasl" {
      for_each = var.authentication_mode == "iam" ? [1] : []
      content {
        iam = true
      }
    }
  }

  open_monitoring {
    prometheus {
      jmx_exporter {
        enabled_in_broker = true
      }
      node_exporter {
        enabled_in_broker = true
      }
    }
  }

  logging_info {
    broker_logs {
      cloudwatch_logs {
        enabled   = true
        log_group = aws_cloudwatch_log_group.this.name
      }
    }
  }

  tags = local.common_tags
}
