output "receipt_role_arn" { value = aws_iam_role.workload["receipt"].arn }
output "notification_role_arn" { value = aws_iam_role.workload["notification"].arn }
output "external_secrets_role_arn" { value = aws_iam_role.workload["secrets"].arn }
output "load_balancer_controller_role_arn" { value = aws_iam_role.workload["load_balancer"].arn }
output "external_dns_role_arn" { value = aws_iam_role.workload["external_dns"].arn }
