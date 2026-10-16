terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "sso_permission_sets" {
  type        = list(string)
  description = "Permission-set names whose SSO roles may mint agent credentials, e.g. [\"AdministratorAccess\"]."
}

variable "role_name" {
  type    = string
  default = "agentbox-agent"
}

data "aws_caller_identity" "this" {}

locals {
  account = data.aws_caller_identity.this.account_id
}

# The agent identity. agent-box assumes it from the engineer's SSO role with a
# session policy and SourceIdentity = agentbox-<session>, so CloudTrail shows the
# agent, not the human. ReadOnlyAccess is the ceiling; the session policy narrows it.
resource "aws_iam_role" "agent" {
  name                 = var.role_name
  max_session_duration = 3600
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::${local.account}:root" }
      Action    = ["sts:AssumeRole", "sts:SetSourceIdentity"]
      Condition = {
        ArnLike    = { "aws:PrincipalArn" = [for p in var.sso_permission_sets : "arn:aws:iam::${local.account}:role/aws-reserved/sso.amazonaws.com/*AWSReservedSSO_${p}_*"] }
        StringLike = { "sts:SourceIdentity" = "agentbox-*" }
      }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "readonly" {
  role       = aws_iam_role.agent.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

output "role_arn" {
  value = aws_iam_role.agent.arn
}
