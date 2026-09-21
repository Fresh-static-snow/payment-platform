output "endpoint" { value = aws_db_instance.this.address }
output "port" { value = aws_db_instance.this.port }
output "database_name" { value = aws_db_instance.this.db_name }
output "master_username" { value = aws_db_instance.this.username }
output "master_user_secret_arn" {
  value       = try(aws_db_instance.this.master_user_secret[0].secret_arn, null)
  description = "AWS-managed Secrets Manager secret containing the master credentials."
}
output "security_group_id" { value = aws_security_group.this.id }
output "kms_key_arn" { value = local.kms_key_arn }
