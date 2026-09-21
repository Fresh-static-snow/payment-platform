variable "cluster_name" {
  type        = string
  description = "EKS cluster name."
}

variable "kubernetes_version" {
  type        = string
  description = "EKS Kubernetes major.minor version."
  default     = "1.32"
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs spanning at least two AZs."

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "At least two private subnets are required."
  }
}

variable "endpoint_public_access" {
  type        = bool
  description = "Expose the Kubernetes API publicly in addition to its private endpoint."
  default     = false
}

variable "public_access_cidrs" {
  type        = list(string)
  description = "CIDRs allowed to reach the public API endpoint."
  default     = []
}

variable "cluster_admin_arns" {
  type        = set(string)
  description = "IAM roles/users granted the AmazonEKSClusterAdminPolicy through EKS access entries."
  default     = []
}

variable "node_groups" {
  description = "Managed node group definitions."
  type = map(object({
    instance_types = list(string)
    capacity_type  = string
    min_size       = number
    max_size       = number
    desired_size   = number
    disk_size      = number
    labels         = map(string)
  }))
  default = {
    general = {
      instance_types = ["m7i.large"]
      capacity_type  = "ON_DEMAND"
      min_size       = 2
      max_size       = 6
      desired_size   = 3
      disk_size      = 80
      labels         = { workload = "general" }
    }
  }

  validation {
    condition = alltrue([
      for group in values(var.node_groups) :
      contains(["ON_DEMAND", "SPOT"], group.capacity_type) &&
      group.min_size >= 0 && group.max_size >= group.min_size &&
      group.desired_size >= group.min_size && group.desired_size <= group.max_size &&
      group.disk_size >= 20
    ])
    error_message = "Each node group must have a valid capacity type, scaling range, and disk size."
  }
}

variable "addons" {
  type        = set(string)
  description = "EKS managed add-ons installed by the module."
  default     = ["vpc-cni", "coredns", "kube-proxy", "eks-pod-identity-agent"]
}

variable "secrets_kms_key_arn" {
  type        = string
  description = "Existing KMS key for Kubernetes Secret envelope encryption; a dedicated key is created when null."
  default     = null
}

variable "log_retention_days" {
  type        = number
  description = "CloudWatch retention for EKS control-plane logs."
  default     = 90
}

variable "tags" {
  type    = map(string)
  default = {}
}
