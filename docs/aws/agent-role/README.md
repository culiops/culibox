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
  agent-box simulates the role's own policy at spawn to check whether it can write, so a
  custom permissions policy must allow `iam:SimulatePrincipalPolicy` on the role itself
  (`ReadOnlyAccess` already does).
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
terraform init   # add -lockfile=readonly to keep the committed provider pins
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

The `role_arn` output is the value for the binding's `role_arn`; `write_role_arn` is the
value for the `elevate` block's `role_arn` (see "Write role" below):

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

## Write role (elevation, M3b)

The same module also creates `agentbox-agent-write` (variable `write_role_name`), the identity
a boxed session works as while a human has granted it write access.

- **Permissions:** the managed policies in `write_policy_arns`, by default `PowerUserAccess`
  and `IAMFullAccess`. That is the ceiling; the elevated session policy (below) narrows it on
  every mint.
- **Trust:** identical to the read role (one shared definition): only the named SSO
  permission sets, only with `SourceIdentity = agentbox-*`.
- **Who mints it:** only a human grant in the agent-box steer rail. Each credential lasts
  15 minutes and is never issued past the grant's window (30 or 60 minutes). The agent cannot
  ask for it on its own.

The binding gets an `elevate` block next to the read role (the `write_role_arn` output is the
value for `elevate.role_arn`):

```json
"aws": {
  "source_profile": "<profile>",
  "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent",
  "region": "<region>",
  "s3_read": ["<state-bucket>"],
  "label": "sandbox",
  "elevate": {
    "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent-write",
    "max_minutes": 60,
    "allow_iam": false
  }
}
```

`elevate.role_arn` must be in the same account and partition as `role_arn` and differ from it.
`max_minutes` is 30 or 60 (default 60). A binding with `elevate` gets one box per session and
refuses shared workspaces: the session needs its own folder or worktree, and a spawn is
refused while any other box overlaps that folder.

**Elevated session policy.** Every write credential carries a session policy: `Allow * *`
intersected with the role's policies, then the same data-read denies as the read role. On top
of that, always-on guard denies keep the window a real bound and the audit trail intact:
`sts:AssumeRole*`, `GetFederationToken` and `GetSessionToken` (a chained credential could
outlive the window), the calls that create long-lived credentials (`iam:CreateAccessKey`,
`CreateLoginProfile`, `UpdateLoginProfile`, `CreateServiceSpecificCredential`,
`UploadSSHPublicKey`), any `iam:*` on `agentbox-*` roles (the agent cannot edit its own
roles), and the CloudTrail changes that would blind the trail (`StopLogging`, `DeleteTrail`,
`UpdateTrail`, `PutEventSelectors`). IAM writes are denied too unless the binding sets
`allow_iam`; `iam:PassRole` stays allowed because ECS and Lambda applies need it.

**Honest limit of `allow_iam: true`:** an agent that can `CreateRole` can create a role that
trusts an outside account. That is persistence past the window, and no session-policy
condition can check trust-policy content. With `allow_iam` on, the window is a bound on
*these credentials*, not on what the agent can set up. The same holds, more weakly, with
`allow_iam` off: resource policies and grants (an S3 bucket policy, a KMS grant, an EC2 key
pair, a Lambda function URL) outlive the window.

### Emergency revoke

A drop or an expiry stops new write credentials at once, but ones already handed out stay
valid until their expiry (at most 15 minutes, never past the window). To cut them off now:
IAM console -> Roles -> `agentbox-agent-write` -> *Revoke sessions* -> *Revoke active
sessions*. That attaches a deny on `aws:TokenIssueTime` to the role; remove the policy
`AWSRevokeOlderSessions` afterwards if you want to keep it tidy.

Check without the console (with credentials for the account): the revoke policy is an inline policy, so
`list-role-policies` shows it; the managed policies (`write_policy_arns`) are listed by
`list-attached-role-policies`.

```bash
aws iam list-role-policies --role-name agentbox-agent-write           # inline: the revoke policy appears here
aws iam list-attached-role-policies --role-name agentbox-agent-write  # managed attachments
```
