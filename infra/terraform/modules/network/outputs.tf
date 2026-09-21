output "vpc_id" {
  value       = aws_vpc.this.id
  description = "VPC ID."
}

output "vpc_cidr" {
  value       = aws_vpc.this.cidr_block
  description = "VPC IPv4 CIDR."
}

output "availability_zones" {
  value       = local.azs
  description = "Availability zones used by the network."
}

output "public_subnet_ids" {
  value       = aws_subnet.public[*].id
  description = "Public subnet IDs for internet-facing load balancers."
}

output "private_subnet_ids" {
  value       = aws_subnet.private[*].id
  description = "Private subnet IDs for EKS nodes."
}

output "data_subnet_ids" {
  value       = aws_subnet.data[*].id
  description = "Isolated subnet IDs for managed data services."
}

output "endpoint_security_group_id" {
  value       = try(aws_security_group.endpoints[0].id, null)
  description = "Security group attached to interface VPC endpoints."
}
