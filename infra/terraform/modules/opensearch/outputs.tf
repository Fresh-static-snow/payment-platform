output "endpoint" { value = "https://${aws_opensearch_domain.this.endpoint}" }
output "domain_arn" { value = aws_opensearch_domain.this.arn }
output "master_secret_arn" { value = aws_secretsmanager_secret.master.arn }
output "security_group_id" { value = aws_security_group.this.id }
output "kms_key_arn" { value = local.kms_key_arn }
