output "repository_urls" {
  description = "Map of service name to ECR repository URL."
  value       = { for name, repository in aws_ecr_repository.this : name => repository.repository_url }
}

output "repository_arns" {
  description = "Map of service name to ECR repository ARN."
  value       = { for name, repository in aws_ecr_repository.this : name => repository.arn }
}
