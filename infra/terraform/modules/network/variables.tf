variable "name" {
  description = "Prefix used for network resource names."
  type        = string
}

variable "vpc_cidr" {
  description = "RFC1918 CIDR for the VPC. The module divides it into /20-style public, private, and data tiers."
  type        = string
  default     = "10.40.0.0/16"

  validation {
    condition     = can(cidrsubnet(var.vpc_cidr, 4, 0)) && can(regex("^(10\\.|172\\.(1[6-9]|2[0-9]|3[01])\\.|192\\.168\\.)", var.vpc_cidr))
    error_message = "vpc_cidr must be a valid RFC1918 IPv4 CIDR with room for at least sixteen subnets."
  }
}

variable "az_count" {
  description = "Number of availability zones. Production should use three."
  type        = number
  default     = 3

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3."
  }
}

variable "enable_nat_gateway" {
  description = "Create NAT gateways for private subnet internet egress."
  type        = bool
  default     = true
}

variable "single_nat_gateway" {
  description = "Use one NAT gateway to reduce non-production cost. Production should use one per AZ."
  type        = bool
  default     = false
}

variable "enable_flow_logs" {
  description = "Send accepted and rejected VPC flow logs to CloudWatch."
  type        = bool
  default     = true
}

variable "flow_log_retention_days" {
  description = "CloudWatch retention for VPC flow logs."
  type        = number
  default     = 90
}

variable "enable_interface_endpoints" {
  description = "Create private endpoints for ECR, CloudWatch Logs, Secrets Manager, and STS."
  type        = bool
  default     = true
}

variable "tags" {
  description = "Tags applied to all resources."
  type        = map(string)
  default     = {}
}
