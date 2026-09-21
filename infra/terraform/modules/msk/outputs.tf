output "cluster_arn" { value = aws_msk_cluster.this.arn }
output "bootstrap_brokers_tls" { value = aws_msk_cluster.this.bootstrap_brokers_tls }
output "bootstrap_brokers_sasl_iam" { value = aws_msk_cluster.this.bootstrap_brokers_sasl_iam }
output "bootstrap_brokers" {
  value       = var.authentication_mode == "iam" ? aws_msk_cluster.this.bootstrap_brokers_sasl_iam : aws_msk_cluster.this.bootstrap_brokers_tls
  description = "Broker list matching authentication_mode."
}
output "authentication_mode" { value = var.authentication_mode }
output "replication_factor" { value = local.replication_factor }
output "security_group_id" { value = aws_security_group.this.id }
output "kms_key_arn" { value = local.kms_key_arn }
