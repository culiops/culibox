# agent role for the credential broker

`main.tf` creates the IAM role a boxed CuliBox session works as when its profile uses an
[AWS connection](../../AWS.md#several-aws-accounts-in-one-session). The broker on the host assumes this role from the engineer's own SSO role,
for 15 minutes at a time, and hands the credentials to the box through `credential_process`.

- **Name:** `agentbox-agent` (variable `role_name`).
- **Permissions:** the AWS-managed `ReadOnlyAccess` policy. That is the ceiling. Each mint
  also carries a session policy generated from the connection, which denies data reads
  (S3 objects outside the connection's `s3_read` allowlist, Secrets Manager values, SSM
  parameters, DynamoDB items, KMS decrypt outside S3). Effective permissions are the
  intersection of the two.
  CuliBox simulates the role's own policy at spawn to check whether it can write, so a
  custom permissions policy must allow `iam:SimulatePrincipalPolicy` on the role itself
  (`ReadOnlyAccess` already does).
- **Trust:** only the account's IAM Identity Center (SSO) roles of the permission sets you
  name in `sso_permission_sets`, and only with a `SourceIdentity` matching `agentbox-*`.
  CuliBox sets `SourceIdentity = RoleSessionName = agentbox-<session id>`, so CloudTrail
  records the agent session, not the human. A plain `aws sts assume-role` without that
  source identity is refused.
- **Session length:** `max_session_duration` is 1 hour. Role chaining (SSO role to this role)
  caps sessions at 1 hour anyway; the broker asks for 15 minutes.

## Apply

Pick the permission set whose SSO profile the connection uses as `source_profile`. Its name is
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

The `role_arn` output is the value for the connection's role; `write_role_arn` is the
value for the elevate role (see "Write role" below).

Then create the connection on the Credentials screen (**Settings → Credentials → Add
credential → Add AWS accounts**): pick the host profile as the source profile, choose the
`agentbox-agent` role (the screen lists the roles the profile can see, or type the ARN) and the
region, and under **Advanced** add `s3_read` buckets if the agent needs to read objects.
The connection applies at once, with no restart. Then tick it in a boxed profile's
**AWS connections** block (the profile must set `"runtime": "box"`). The same connection in
`config.json`, which is read at daemon start:

```json
"aws_connections": [
  {
    "name": "sandbox",
    "source_profile": "<profile>",
    "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent",
    "region": "<region>",
    "s3_read": ["<state-bucket>", "<log-bucket>/AWSLogs/"],
    "label": "sandbox"
  }
]
```

```json
{ "name": "claude-aws", "bin": "claude", "runtime": "box", "aws_connections": ["sandbox"] }
```

Older profiles may still carry the same fields inline, as an `"aws": { ... }` object on the
profile. That form still works; the UI shows it as **Old-style AWS** and offers **Convert to a
connection**. Use a connection for anything new.

## Which accounts

Only a sandbox or lab account for now. **Do not apply this to a company or production
account until CuliBox can redact command output, which it cannot yet:** with read access an agent can still pull data
into its transcript that `ReadOnlyAccess` allows and the session policy does not deny
(Lambda code URLs, ECR layers, Athena and Glue results, CloudWatch Logs, among others).

## SSO-direct mode (read-only permission sets)

Everything above is the **agent role** mode: the broker assumes `agentbox-agent` and adds a
session policy. It is the recommended mode and the default. Use it whenever you can create the
role.

Some accounts do not let you create `agentbox-agent` (a company account, say). For those a
connection can opt in to `"mode": "sso_direct"`: the agent gets the SSO permission set's **own**
credentials, with no role to create. It is opt-in per connection, for permission sets that pass a
read-only gate (a check of the permission set's own identity policies; see Known limits below),
and it gives up the protections of the agent role:

- **No narrowing.** There is no session policy and no S3-read scoping. The agent can do
  whatever the permission set allows, and nothing narrows it further.
- **No elevate.** A connection in this mode cannot be widened to a write role (`role_arn`,
  `elevate` and `s3_read` must be empty; the save is refused otherwise).
- **Lifetime.** The credentials keep the expiry of your SSO role credentials (up to N hours,
  N shown in the UI), not 15 minutes. They cannot be revoked when the agent session ends.
- **Attribution.** CloudTrail shows **you**, not an `agentbox-*` session.

The UI says this next to the mode choice (`Agent role (recommended)` / `SSO role directly`):

> SSO role directly: the agent gets your permission set's own credentials, valid for up to N h. CloudTrail shows you, not the agent, and they cannot be revoked when the session ends. Read-only permission sets only.

On the Credentials screen this is **Credentials the agent gets → SSO role directly**. In
`config.json`:

```json
"aws_connections": [
  {
    "name": "company-readonly",
    "mode": "sso_direct",
    "source_profile": "<sso-profile>",
    "region": "<region>",
    "label": "company-readonly"
  }
]
```

`source_profile` must be a pure IAM Identity Center profile (`sso_session` or `sso_account_id`
in `~/.aws/config`). A profile with static keys, a `credential_process`, a `credential_source`
or an assume-role chain is refused at save, spawn and every mint, so this mode never hands out
a long-lived key. 
The "Which accounts" caveat applies to this mode too: agent output can carry anything the
permission set can read, and output redaction has not shipped.

### The read-only gate

A permission set is used only when the **gate** passes it (see Known limits for what that
does and does not cover). Only a definite "read-only"
lets credentials out, a save happen or a spawn proceed. Anything the gate cannot verify (SSO
expired, a timeout, a throttled call, a missing permission, a policy it cannot parse) refuses.
There is no "I understand" override. The gate runs when you save the connection, when a session
spawns, in `culibox doctor`, and again on **every** credential mint, a refresh included
(about 5 minutes before the credentials expire). A permission set that gains a write
mid-session is refused at the next mint. Credentials already issued keep working until they
expire (up to N h), and AWS applies the permission set's current policy to them immediately;
only the gate's re-check waits for the next mint.

What it does, in plain words:

1. Checks that the credentials are an Identity Center permission-set session in the profile's
   own account, then reads the role's real ARN.
2. Reads the permission set's **own** policies: every inline policy and every attached managed
   policy (default version, every page). A permission boundary only restricts, so it is not read.
3. Works out which Allow patterns are left after the permission set's own Denies. Only an
   **unconditional** Deny counts: no `Condition`, no `NotResource`, `Resource: "*"`. An Allow with
   `NotAction`, or without `Action`, refuses.
4. Checks every remaining Allow pattern. It must be a read verb (`Get`, `List`, `Describe`,
   `BatchGet`, `BatchDescribe`, `Search`, `Lookup`, `View`, `Query`, `Scan`, `Select`; case is
   ignored, so `apigateway:GET` is one) or on a reviewed list of non-standard verbs, each marked
   harmless or side-effecting (for example `logs:StartQuery` is harmless; `sqs:Receive*` is not,
   because `sqs:ReceiveMessage` hides messages from your real consumers). `*`, `s3:*` and an
   unknown verb refuse.
5. Screens every action the remaining patterns cover for **credential minting**, against an
   embedded copy of AWS's action catalog (see below): any action whose name contains
   credential, token, password, access, key, secret, session, login, auth or url must be
   denied, unless it is on a reviewed harmless list (for example `iam:GetAccessKeyLastUsed`).
   A second reviewed list adds secret-returning reads the name does not give away:
   `lambda:GetFunction` and `lambda:GetLayerVersion` (presigned code URLs),
   `cognito-idp:DescribeUserPoolClient` (client secret), `apigateway:GET` (API key values),
   and `sts:AssumeRole`, `AssumeRoleWithSAML`, `AssumeRoleWithWebIdentity` and `AssumeRoot`.
6. Runs an IAM policy simulation (`iam:SimulatePrincipalPolicy`, sent in batches of 100 actions)
   for the 11 broad write actions the base-role check uses, every side-effect action from step 4,
   and every action from step 5. Each must come back denied. **"Denied" is strict:** the
   simulation must say `explicitDeny` *and* an unconditional `Resource: "*"` Deny statement in
   the permission set's own policies must cover the action, or the simulation must say
   `implicitDeny` with no Allow in the permission set covering it. A conditional or tag-based
   Deny, a Deny that only lives in an SCP or a permission boundary, or a simulation that needs
   condition values it cannot know, does not count.
7. Reports, without blocking, which data reads (Secrets Manager values, SSM parameters,
   DynamoDB items, S3 objects, KMS decrypt) the permission set denies and which it still allows.

The refusal lists **every** refusing pattern or action, not only the first, so one guardrail can
be written from it.

**The permission set needs to read itself.** The gate calls, with the permission set's own
credentials, `sts:GetCallerIdentity`, `iam:GetRole`, `iam:ListRolePolicies`,
`iam:GetRolePolicy`, `iam:ListAttachedRolePolicies`, `iam:GetPolicy` and
`iam:GetPolicyVersion`, plus `iam:SimulatePrincipalPolicy` on its own role. `ReadOnlyAccess`
includes them. A custom permission set that lacks one is refused, and the message names the
IAM actions to grant (the simulation is the one step that reports `role cannot simulate itself`
instead).

### ReadOnlyAccess alone is refused

The AWS-managed `ReadOnlyAccess` is the obvious candidate, and on its own it **fails the gate**.
Despite the name it allows reads that hand the caller something it should not have:

- `sqs:ReceiveMessage` (hides messages from production consumers and counts toward redrive),
- `ec2:GetPasswordData`,
- `ecr:GetAuthorizationToken` and `codeartifact:GetAuthorizationToken`,
- `cognito-identity:GetCredentialsForIdentity` and `GetOpenIdToken`,
- `s3:GetDataAccess`, `ssm:GetAccessToken`,
- `lambda:GetFunction` (a presigned URL to the code package),
- `cognito-idp:DescribeUserPoolClient`, and `apigateway:GET`,
- and many more: as of `ReadOnlyAccess` v190 and the catalog snapshot below, roughly 270 actions.

`ViewOnlyAccess` is refused for the same kind of reason. To use a managed read policy, add an
**inline Deny** to the same permission set. A guardrail passes when its Deny statements have no
`Condition`, no `NotResource` and `Resource: "*"`, and between them cover every action in the
refusal: `sqs:ReceiveMessage`, the credential-minting reads, and the secret-returning reads. A
tag- or principal-based exception is refused. A `NotAction` Deny that lists only the services you
keep is accepted too. Denying data reads (Secrets Manager values, SSM parameters, DynamoDB items,
S3 objects, KMS decrypt) is optional: the gate only reports them.

An illustration of the shape, **not a complete list**: take the full list from your own refusal
message, because it depends on the policy version and on the catalog snapshot.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AgentBoxReadOnlyGuardrail",
      "Effect": "Deny",
      "Resource": "*",
      "Action": [
        "sqs:ReceiveMessage",
        "ec2:GetPasswordData",
        "ecr:GetAuthorizationToken",
        "codeartifact:GetAuthorizationToken",
        "cognito-identity:GetCredentialsForIdentity",
        "cognito-identity:GetOpenIdToken",
        "cognito-idp:DescribeUserPoolClient",
        "s3:GetDataAccess",
        "ssm:GetAccessToken",
        "lambda:GetFunction",
        "lambda:GetLayerVersion",
        "apigateway:GET"
      ]
    }
  ]
}
```

`apigateway:GET` is on the list because it also returns API key values, so a permission set
passes only if it denies all API Gateway reads. That is the price of an unconditional rule.

### Known limits

The gate is a strong check on the permission set's identity policies. It is **not** proof that
the agent can do no harm:

- **Resource-based policies are invisible.** A bucket, queue or key policy that grants your
  role access is not seen by the gate: AWS cannot simulate resource policies for roles. A bucket
  policy that lets your role write is a write the gate cannot find.
- **SCPs and permission boundaries do not count.** A Deny that lives only there is not
  something the gate can inspect, so it never turns a refusal into a pass.
- **Secrets stored in configuration are only reported, never blocked.** Lambda environment
  variables, EC2 user data and launch templates, CloudFront origin headers and SSM parameters
  are readable through ordinary `Get*`/`Describe*` calls. The data-read report (step 7) says
  what the permission set can still read; it does not stop it.
- **New AWS actions are unscreened until the snapshot is refreshed.** The credential-minting
  screen expands wildcards against an embedded copy of the AWS service reference taken on
  **2026-10-30** (`CatalogDate` in `internal/awsprobe/catalog.go`). An action AWS adds later,
  under a wildcard your permission set already allows, is not screened until the snapshot is
  refreshed. A *named* action missing from the snapshot refuses. Refresh with
  `go run ./internal/awsprobe/gencatalog`, update `CatalogDate`, and rerun the tests. There is
  no automatic age check.
- **The secret-returning list is hand-reviewed.** A read that returns a secret under a name the
  screen does not catch, and that is not on the list, passes. Report it.
- **Retired services.** The catalog no longer lists `codestar`, `deepcomposer`, `evidently`,
  `iot1click` and `iotevents`, which `ReadOnlyAccess` still grants. Those patterns are skipped
  by the screen (they expand to nothing) instead of being reported as unknown.
- Credentials already issued stay valid until their own expiry (shown in the UI as up to N h),
  after the agent session ends or the permission set changes.

## Known limits between sessions

- **Between boxes:** a broker token works only from the box its session runs in.
  Presented from any other box it is refused and audited as `denied_box`, so a
  token that leaks into another box is no use there.
- **Within one box:** processes share a uid and can read each other's
  `/proc/*/environ`, so one session can present another's broker token. Attribution
  (the audit and CloudTrail `SourceIdentity`) is best-effort between sessions that share a
  box (same connections).
- **Shared workspace:** two sessions in the same project folder get separate boxes but
  share that folder read-write. An unbound session, or one with different connections, can
  plant a file the bound session will run (for example under `.terraform/`, a git hook, a
  script). That code runs inside the bound box, can read its token and credentials from the
  environment, and so gets the bound session's access. Give a bound session its own
  workspace.

## Write role (elevation)

The same module also creates `agentbox-agent-write` (variable `write_role_name`), the identity
a boxed session works as while a human has granted it write access.

- **Permissions:** the managed policies in `write_policy_arns`, by default `PowerUserAccess`
  and `IAMFullAccess`. That is the ceiling; the elevated session policy (below) narrows it on
  every mint.
- **Trust:** identical to the read role (one shared definition): only the named SSO
  permission sets, only with `SourceIdentity = agentbox-*`.
- **Who mints it:** only a human grant in the CuliBox steer rail. Each credential lasts
  15 minutes and is never issued past the grant's window (30 or 60 minutes). The agent cannot
  ask for it on its own.

The connection gets an `elevate` block next to the read role (the `write_role_arn` output is the
value for `elevate.role_arn`; on the Credentials screen it is the **Elevate role ARN** under
**Advanced**). In `config.json`:

```json
"aws_connections": [
  {
    "name": "sandbox",
    "source_profile": "<profile>",
    "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent",
    "region": "<region>",
    "s3_read": ["<state-bucket>"],
    "label": "sandbox",
    "elevate": {
      "role_arn": "arn:aws:iam::123456789012:role/agentbox-agent-write",
      "max_minutes": 60,
      "allow_iam": false,
      "box_bucket_prefix": "culibox-",
      "pass_role_prefix": "culibox-"
    }
  }
]
```

`elevate.role_arn` must be in the same account and partition as `role_arn` and differ from it.
`max_minutes` is 30 or 60 (default 60). `box_bucket_prefix` is optional and off when left out
(see the box-bucket exception below). `pass_role_prefix` is optional too: left out, the window
cannot pass any IAM role (see below). A connection with `elevate` gets one box per session and
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
`UpdateTrail`, `PutEventSelectors`). IAM writes are denied too unless the connection sets
`allow_iam`.

**`iam:PassRole` is denied unless you name a prefix.** A service that runs as a passed role
(Lambda, ECS, Glue, CodeBuild, Step Functions) reads whatever that role can read, and the session
policy never limits it, so allowing PassRole on any role makes the data-read deny advisory
([#302](https://github.com/culiops/culibox/issues/302)). Left out, `pass_role_prefix` denies `iam:PassRole` on every role: a stack that creates a
Lambda function, an ECS task or an instance profile fails to apply in the window. With
`"pass_role_prefix": "culibox-"` the window may pass only roles named `culibox-*` (3 to 40 IAM
role-name characters, no wildcards, no `/`; `agentbox-`, `aws` and `service-role` stems are
refused). The match is on the text after `role/`, and `*` also matches `/`: `role/culibox-x/Admin` matches `culibox-*`, `role/team/culibox-x` does not. Name the roles your labs use with that
prefix. Passing is not creating: a role of that name must already exist, or `allow_iam` must be
on. **With `allow_iam` on, the agent can create a role of that name or any trust and use it, so
the data-read blocks are advisory in that window**; the grant panel and the editor say so.

Two exceptions let Terraform plan and destroy in the window; the read role has neither:

- **AWS public parameters.** `ssm:GetParameter`, `GetParameters`, `GetParametersByPath` and `GetParameterHistory`
  work on AWS's public parameters under `/aws/service/` (AMI ids, the ECS-optimized AMI), so a
  `data "aws_ssm_parameter"` on one plans. Every parameter in your account stays denied: a
  public parameter's ARN has an empty account field (`arn:aws:ssm:<region>::parameter/aws/service/...`),
  yours carry the account id, and SSM refuses names that start with `aws`.
- **Box buckets, opt-in.** With `"box_bucket_prefix": "culibox-"` in `elevate`, `s3:GetObject`
  and `GetObjectVersion` also work in buckets whose name starts with the prefix, the buckets the
  box creates, so Terraform can refresh the `aws_s3_object`s it uploaded. The prefix is 3 to 40
  lowercase letters, digits and `-`, ends in `-`, and has no wildcards. Stems that AWS tools
  create in shared buckets (`aws`, `cdk-`, `sagemaker-`, `elasticbeanstalk-`, `amplify-`,
  `cf-templates-`, `codepipeline-`, `config-bucket-`) and `agentbox-` are refused. Off by default.

**What `box_bucket_prefix` opens, honestly.** It weakens the window's read deny in two ways:

- **Every bucket whose name starts with the prefix is readable in the window.** That includes
  one a human created with that name, and one in another account, because an S3 ARN has no
  account. Pick a prefix that only the box uses.
- **Data copied into a box bucket becomes readable to the agent.** A direct copy
  (`CopyObject`, `UploadPartCopy`) needs `s3:GetObject` on the source, which stays denied, and a
  bucket policy or ACL cannot grant the session more: an explicit deny in the session policy wins
  over any allow. Managed copies are different. A DynamoDB export runs with your S3 write, and
  the other managed copies run as a role the session passed with `iam:PassRole` (allowed only on
  `pass_role_prefix` roles, if you set one). The session policy never sees those reads. So the
  elevated policy always denies ([#301](https://github.com/culiops/culibox/issues/301)):
  - `dynamodb:ExportTableToPointInTime` (table export to S3);
  - `rds:StartExportTask` (snapshot or cluster export to S3);
  - `s3:CreateJob` (S3 Batch Operations, including Batch Replication);
  - `datasync:StartTaskExecution`;
  - `backup:StartRestoreJob` (an S3 restore can target any bucket). This also blocks restores
    of other resource types from AWS Backup in the window;
  - `s3:PutReplicationConfiguration`. With `box_bucket_prefix` set it is allowed on a box bucket,
    because replication *from* a box bucket moves only box data. Without it there is no
    exception: an `s3_read` entry may name just a prefix of a bucket, and replication copies the
    whole bucket.

  These denies are always on, whatever `s3_read` and `box_bucket_prefix` say: a DynamoDB export
  needs no PassRole and can write to a bucket outside the account that the agent then reads
  without signing. A lab that needs one of these actions cannot run it in a write window.

  **Related route: a public bucket.** `s3:PutBucketPolicy`, `PutBucketAcl` and
  `PutPublicAccessBlock` stay allowed in the window. The agent can make a bucket public, so
  anything the window can read (an `s3_read` bucket, a box bucket) can be exposed to the
  internet from there. A bucket the session may not read can be exposed too: public access
  settings, `PutBucketPolicy`, `PutBucketAcl` and `PutObjectAcl` are not limited by the data-read
  deny, so data that reaches a bucket by another route can be read unsigned. This is a known gap,
  tracked in [#307](https://github.com/culiops/culibox/issues/307).

**What the opt-in does not change.** Code run as a passed role (a Lambda function, an ECS task,
an EC2 instance profile, a Glue or CodeBuild job) reads whatever that role can read and can hand
it back without any bucket. Without `pass_role_prefix` the window cannot pass a role, so this
needs a role of the prefix (which `allow_iam` lets the agent create), or a compute resource that
already carries a role. Exports of data the window never denied (CloudWatch Logs, EC2 images, RDS
rows read through SQL) are not denied either: denying them would protect nothing.

**Why there is no tag-based exception for secrets, parameters, DynamoDB items or KMS.**
Terraform tags what it creates, so "read what carries the box tag" looks natural. But in
Secrets Manager, Parameter Store, DynamoDB and KMS, tagging a resource as you create it needs
the same permission as tagging an existing one (`secretsmanager:TagResource`,
`ssm:AddTagsToResource`, `dynamodb:TagResource`, `kms:TagResource`), and no condition key tells
the two apart. A session policy could not let Terraform tag a new secret without also letting
the agent put the box tag on a human's secret and then read it. Secret values, your own
parameters, DynamoDB items and KMS decryption (other than through S3) therefore stay denied in
the window. A stack whose plan reads one of them cannot run in a write window.

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
