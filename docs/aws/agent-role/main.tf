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

variable "write_role_name" {
  type    = string
  default = "agentbox-agent-write"
}

variable "write_policy_arns" {
  type        = list(string)
  description = "Managed policies on the write role. The elevated session policy (M3b) trims IAM writes unless allow_iam, and always denies credential minting, edits to agentbox-* roles, CloudTrail changes and iam:PassRole (except on roles named elevate.pass_role_prefix*)."
  default = [
    "arn:aws:iam::aws:policy/PowerUserAccess",
    "arn:aws:iam::aws:policy/IAMFullAccess",
  ]
}

locals {
  account = data.aws_caller_identity.this.account_id
  # One trust policy for both roles: only the named SSO permission sets, and only
  # with SourceIdentity = agentbox-<session>.
  agent_trust = jsonencode({
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

# The agent identity. CuliBox assumes it from the engineer's SSO role with a
# session policy and SourceIdentity = agentbox-<session>, so CloudTrail shows the
# agent, not the human. ReadOnlyAccess is the ceiling; the session policy narrows it.
resource "aws_iam_role" "agent" {
  name                 = var.role_name
  max_session_duration = 3600
  assume_role_policy   = local.agent_trust
}

resource "aws_iam_role_policy_attachment" "readonly" {
  role       = aws_iam_role.agent.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

output "role_arn" {
  value = aws_iam_role.agent.arn
}

# The write identity (M3b). Only a human grant in the CuliBox UI mints it, for
# 15 minutes at a time and never past the grant's window; the session policy
# narrows it further.
resource "aws_iam_role" "agent_write" {
  name                 = var.write_role_name
  max_session_duration = 3600
  assume_role_policy   = local.agent_trust
}

resource "aws_iam_role_policy_attachment" "write" {
  for_each   = toset(var.write_policy_arns)
  role       = aws_iam_role.agent_write.name
  policy_arn = each.value
}

output "write_role_arn" {
  value = aws_iam_role.agent_write.arn
}
