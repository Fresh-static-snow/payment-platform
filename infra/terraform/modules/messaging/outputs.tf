output "receipt_bucket_name" { value = aws_s3_bucket.receipts.id }
output "receipt_bucket_arn" { value = aws_s3_bucket.receipts.arn }
output "notification_topic_arn" { value = aws_sns_topic.notifications.arn }
output "notification_queue_url" { value = aws_sqs_queue.notifications.id }
output "notification_queue_arn" { value = aws_sqs_queue.notifications.arn }
output "notification_dlq_url" { value = aws_sqs_queue.dlq.id }
output "notification_dlq_arn" { value = aws_sqs_queue.dlq.arn }
output "kms_key_arn" { value = local.kms_key_arn }
