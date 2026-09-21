data "aws_partition" "current" {}

locals {
  oidc_host   = replace(var.oidc_provider_url, "https://", "")
  common_tags = merge(var.tags, { Component = "iam" })
  service_accounts = {
    receipt       = "receipt-service"
    notification  = "notification-service"
    secrets       = "external-secrets"
    load_balancer = "aws-load-balancer-controller"
    external_dns  = "external-dns"
  }
  service_account_namespaces = {
    receipt       = var.namespace
    notification  = var.namespace
    secrets       = "external-secrets"
    load_balancer = "kube-system"
    external_dns  = "external-dns"
  }
}

data "aws_iam_policy_document" "assume" {
  for_each = local.service_accounts

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = ["system:serviceaccount:${local.service_account_namespaces[each.key]}:${each.value}"]
    }
  }
}

resource "aws_iam_role" "workload" {
  for_each = local.service_accounts

  name_prefix        = "${var.name}-${replace(each.key, "_", "-")}-"
  assume_role_policy = data.aws_iam_policy_document.assume[each.key].json
  tags               = merge(local.common_tags, { Workload = each.key })
}

data "aws_iam_policy_document" "receipt" {
  statement {
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [var.receipt_bucket_arn]
  }
  statement {
    actions   = ["s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload"]
    resources = ["${var.receipt_bucket_arn}/receipts/*"]
  }
  statement {
    actions   = ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey"]
    resources = var.receipt_kms_key_arns
  }
}

resource "aws_iam_role_policy" "receipt" {
  name   = "receipt-storage"
  role   = aws_iam_role.workload["receipt"].id
  policy = data.aws_iam_policy_document.receipt.json
}

data "aws_iam_policy_document" "notification" {
  statement {
    actions   = ["sns:Publish"]
    resources = [var.notification_topic_arn]
  }
  statement {
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
    ]
    resources = var.notification_queue_arns
  }
  statement {
    actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
    resources = var.notification_kms_key_arns
  }
}

resource "aws_iam_role_policy" "notification" {
  name   = "notification-delivery"
  role   = aws_iam_role.workload["notification"].id
  policy = data.aws_iam_policy_document.notification.json
}

data "aws_iam_policy_document" "secrets" {
  statement {
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = var.application_secret_arns
  }
  statement {
    actions   = ["kms:Decrypt"]
    resources = var.secret_kms_key_arns
  }
}

resource "aws_iam_role_policy" "secrets" {
  name   = "read-application-secrets"
  role   = aws_iam_role.workload["secrets"].id
  policy = data.aws_iam_policy_document.secrets.json
}

resource "aws_iam_role_policy" "load_balancer" {
  name = "aws-load-balancer-controller"
  role = aws_iam_role.workload["load_balancer"].id
  policy = replace(
    file("${path.module}/policies/aws-load-balancer-controller-v3.5.0.json"),
    "arn:aws:",
    "arn:${data.aws_partition.current.partition}:",
  )
}

data "aws_iam_policy_document" "external_dns" {
  statement {
    actions   = ["route53:ChangeResourceRecordSets"]
    resources = var.hosted_zone_arns
  }
  statement {
    actions   = ["route53:ListHostedZones", "route53:ListHostedZonesByName", "route53:ListResourceRecordSets", "route53:ListTagsForResource"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "external_dns" {
  count = length(var.hosted_zone_arns) > 0 ? 1 : 0

  name   = "external-dns"
  role   = aws_iam_role.workload["external_dns"].id
  policy = data.aws_iam_policy_document.external_dns.json
}
