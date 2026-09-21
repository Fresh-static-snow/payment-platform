data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
data "aws_partition" "current" {}

locals {
  common_tags = merge(var.tags, { Component = "messaging" })
  kms_key_arn = coalesce(var.kms_key_arn, try(aws_kms_key.this[0].arn, null))
  bucket_name = substr(lower(replace("${var.name}-${data.aws_caller_identity.current.account_id}-${data.aws_region.current.region}-receipts", "_", "-")), 0, 63)
}

data "aws_iam_policy_document" "kms" {
  statement {
    sid       = "AccountAdministrationAndIAMDelegation"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  statement {
    sid = "AllowMessagingServices"
    actions = [
      "kms:Decrypt",
      "kms:DescribeKey",
      "kms:GenerateDataKey",
      "kms:ReEncryptFrom",
      "kms:ReEncryptTo",
    ]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com", "sns.amazonaws.com", "sqs.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_kms_key" "this" {
  count = var.kms_key_arn == null ? 1 : 0

  description             = "${var.name} S3/SNS/SQS encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.kms.json
  tags                    = local.common_tags
}

resource "aws_kms_alias" "this" {
  count         = var.kms_key_arn == null ? 1 : 0
  name          = "alias/${var.name}-messaging"
  target_key_id = aws_kms_key.this[0].key_id
}

resource "aws_s3_bucket" "receipts" {
  bucket        = local.bucket_name
  force_destroy = var.force_destroy
  tags          = merge(local.common_tags, { DataClassification = "confidential" })
}

resource "aws_s3_bucket_public_access_block" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "receipts" {
  bucket = aws_s3_bucket.receipts.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "receipts" {
  bucket = aws_s3_bucket.receipts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "receipts" {
  bucket = aws_s3_bucket.receipts.id
  rule {
    apply_server_side_encryption_by_default {
      kms_master_key_id = local.kms_key_arn
      sse_algorithm     = "aws:kms"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  rule {
    id     = "receipt-retention"
    status = "Enabled"

    filter {}

    expiration {
      days = var.receipt_retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = 90
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.receipts]
}

data "aws_iam_policy_document" "receipts" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.receipts.arn,
      "${aws_s3_bucket.receipts.arn}/*",
    ]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "receipts" {
  bucket = aws_s3_bucket.receipts.id
  policy = data.aws_iam_policy_document.receipts.json

  depends_on = [aws_s3_bucket_public_access_block.receipts]
}

resource "aws_sns_topic" "notifications" {
  name              = "${var.name}-notifications"
  kms_master_key_id = local.kms_key_arn
  tags              = local.common_tags
}

resource "aws_sqs_queue" "dlq" {
  name                      = "${var.name}-notifications-dlq"
  message_retention_seconds = 1209600
  kms_master_key_id         = local.kms_key_arn
  tags                      = local.common_tags
}

resource "aws_sqs_queue" "notifications" {
  name                       = "${var.name}-notifications"
  message_retention_seconds  = var.message_retention_seconds
  visibility_timeout_seconds = 60
  receive_wait_time_seconds  = 20
  kms_master_key_id          = local.kms_key_arn
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.max_receive_count
  })
  tags = local.common_tags
}

resource "aws_sqs_queue_redrive_allow_policy" "notifications" {
  queue_url = aws_sqs_queue.dlq.id
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.notifications.arn]
  })
}

data "aws_iam_policy_document" "notification_queue" {
  statement {
    sid       = "AllowSNSTopic"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.notifications.arn]
    principals {
      type        = "Service"
      identifiers = ["sns.amazonaws.com"]
    }
    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_sns_topic.notifications.arn]
    }
  }
}

resource "aws_sqs_queue_policy" "notifications" {
  queue_url = aws_sqs_queue.notifications.id
  policy    = data.aws_iam_policy_document.notification_queue.json
}

resource "aws_sns_topic_subscription" "notifications" {
  topic_arn            = aws_sns_topic.notifications.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.notifications.arn
  raw_message_delivery = true
}
