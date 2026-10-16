# agent role for the credential broker

`main.tf` creates the IAM role a boxed agent-box session works as when its profile has an
`aws` binding. The broker on the host assumes this role from the engineer's own SSO role,
for 15 minutes at a time, and hands the credentials to the box through `credential_process`.

- **Name:** `agentbox-agent` (variable `role_name`).
- **Permissions:** the AWS-managed `ReadOnlyAccess` policy. That is the ceiling. Each mint
  also carries a session policy generated from the binding, which denies data reads
  (S3 objects outside the binding's `s3_read` allowlist, Secrets Manager values, SSM
  parameters, DynamoDB items, KMS decrypt outside S3). Effective permissions are the
  intersection of the two.
- **Trust:** only the account's IAM Identity Center (SSO) roles of the permission sets you
  name in `sso_permission_sets`, and only with a `SourceIdentity` matching `agentbox-*`.
  agent-box sets `SourceIdentity = RoleSessionName = agentbox-<session id>`, so CloudTrail
  records the agent session, not the human. A plain `aws sts assume-role` without that
  source identity is refused.
- **Session length:** `max_session_duration` is 1 hour. Role chaining (SSO role to this role)
  caps sessions at 1 hour anyway; the broker asks for 15 minutes.

## Apply

Pick the permission set whose SSO profile the binding uses as `source_profile`. Its name is
the middle part of the role in `aws sts get-caller-identity --profile <profile>`:
`assumed-role/AWSReservedSSO_<PermissionSet>_<suffix>/<you>`.

That permission set must itself allow `sts:AssumeRole` and `sts:SetSourceIdentity` on this
role (an administrator permission set does). Then:

```bash
aws sts get-caller-identity --profile <profile> || aws sso login --profile <profile>
terraform init
terraform validate
AWS_PROFILE=<profile> terraform plan -var 'sso_permission_sets=["<PermissionSet>"]' \
  -state="$HOME/.local/state/agentbox/agent-role.tfstate" -out tf.plan
AWS_PROFILE=<profile> terraform apply -state="$HOME/.local/state/agentbox/agent-role.tfstate" tf.plan
rm -f tf.plan
```

Keep the state outside this checkout (the `-state` path above, or your own backend), so it
outlives the checkout. Check first that no role of that name exists
(`aws iam get-role --role-name agentbox-agent` must say `NoSuchEntity`); this file does not
import an existing role.

The `role_arn` output is the value for the binding's `role_arn`:

```json
"aws": {
  "source_profile": "<profile>",
  "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent",
  "region": "<region>",
  "s3_read": ["<state-bucket>", "<log-bucket>/AWSLogs/"],
  "label": "sandbox"
}
```

The profile must also set `"runtime": "box"`.

## Which accounts

Only a sandbox or lab account for now. **Do not apply this to a company or production
account until output redaction (M3c) ships:** with read access an agent can still pull data
into its transcript that `ReadOnlyAccess` allows and the session policy does not deny
(Lambda code URLs, ECR layers, Athena and Glue results, CloudWatch Logs, among others).

## Known limits between sessions

- **Within one box:** processes share a uid and can read each other's
  `/proc/*/environ`, so one session can present another's broker token. Attribution
  (the audit and CloudTrail `SourceIdentity`) is best-effort between sessions that share a
  box (same binding).
- **Shared workspace:** two sessions in the same project folder get separate boxes but
  share that folder read-write. An unbound session, or one with a different binding, can
  plant a file the bound session will run (for example under `.terraform/`, a git hook, a
  script). That code runs inside the bound box, can read its token and credentials from the
  environment, and so gets the bound session's access. Give a bound session its own
  workspace.
