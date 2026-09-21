provider "aws" {
  region              = var.aws_region
  allowed_account_ids = var.aws_account_id == null ? null : [var.aws_account_id]

  default_tags {
    tags = {
      Project     = "payment-platform"
      Environment = "dev"
      ManagedBy   = "terraform"
    }
  }
}
