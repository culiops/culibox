# AWS access

How a credential reaches the agent. The profile and role names in it are examples.

![How an AWS credential reaches the agent, in seven steps. 1: you sign in with AWS SSO on the host, and the SSO session stays there. 2: a command in the box asks the CuliBox broker over its own socket, carrying no AWS secret. 3: the broker assumes a read-only role, and a session policy narrows it further. 4: AWS returns temporary credentials. 5: the box gets credentials that expire in 15 minutes, and a write role only in a window you grant. 6: the mint is recorded in the audit log (session, account, role), never the value. 7: CloudTrail records the agent session, not you.](images/aws-broker-flow.svg)

## Several AWS accounts in one session

An **AWS connection** is a saved, named way into one AWS account: its own source profile, role, region, session policy, egress hosts and optional write role. You create connections once, on the Credentials screen, and a boxed profile lists the ones it may use by name. A profile can list several, so one session can reach a staging account and a production account, say. Like the other credential bindings, a bound spawn is refused on the host tier, and on macOS it goes through the egress allowlist (the allowlist is verified on a Mac; AWS credentials through the broker there are not yet; see [docs/MACOS.md](MACOS.md#every-box)).

**1. Create the connections.** Open **Settings → Credentials**, choose **Add credential → Add AWS accounts**, and pick the host AWS profiles to use as the source profile (the screen can start the SSO sign-in for one that has expired). For each, choose the role, the region and, under **Advanced**, extra egress hosts, S3 read and an elevate role. Saving runs the base-role check below. The connection is stored in `credentials.json` in CuliBox's data directory and takes effect at once: no restart is needed, and a profile or a new session sees it straight away (sessions already running keep the definition they were spawned with).

Connections can also be declared in `config.json`, next to `static_tokens`. These are read at daemon start, so adding or changing one needs a restart. The Credentials screen lists them as defined in `config.json` and does not edit or delete them:

```json
"aws_connections": [
  { "name": "staging-read", "label": "Staging", "description": "Staging account, read-only",
    "source_profile": "acme-sso", "region": "eu-west-1",
    "role_arn": "arn:aws:iam::111111111111:role/agentbox-agent" },
  { "name": "prod-read", "label": "PROD", "prod": true, "description": "Production account; writes need a granted window",
    "source_profile": "acme-sso", "region": "eu-west-1",
    "role_arn": "arn:aws:iam::222222222222:role/agentbox-agent",
    "elevate": { "role_arn": "arn:aws:iam::222222222222:role/agentbox-agent-write", "max_minutes": 30 } }
]
```

Each entry takes `source_profile`, `role_arn`, `region`, `s3_read`, `egress_allow`, `label`, `prod` and `elevate` (see [docs/aws/agent-role](aws/agent-role/README.md) for the roles), plus:

| Field | Rules |
|---|---|
| `name` | Required, unique. `^[a-z0-9][a-z0-9-]{0,62}$`. `default` and `agentbox` are reserved. Profiles refer to the connection by it, and it is the AWS profile name inside the box. |
| `description` | Optional, at most 200 characters, one line. Shown in the UI and in `agentbox-env --list`. |
| `region` | Required for every connection. |
| `mode` | Optional. Absent means the agent role. `"sso_direct"` uses the SSO permission set directly (see [SSO role directly](#sso-role-directly-opt-in)); then `role_arn`, `elevate` and `s3_read` must be left out. |

**2. Pick them in a profile.** Set the profile's runtime to the box, then tick the connections in the profile's **AWS connections** block. In `config.json` or the profile's JSON, set `"runtime": "box"` explicitly and list the names:

```json
{ "name": "claude-aws", "bin": "claude", "runtime": "box", "aws_connections": ["staging-read", "prod-read"] }
```

A profile binds at most 16 connections, with no duplicates, and each must be defined. Saving or spawning a profile that names an undefined connection is refused. A connection that a profile still uses cannot be deleted.

**Older profiles with an inline `aws` binding.** Before saved connections, a profile carried its AWS settings inline, as an `"aws": { ... }` object. Such a profile still works unchanged. The UI shows it as **Old-style AWS** and offers **Convert to a connection** (**Move to a saved connection** on the profile page): CuliBox creates a connection from the binding (named after the profile) and points the profile at it, and the profile's next session gets a new box. A profile uses either the inline binding or `aws_connections`, never both, so convert or remove the inline binding before adding connections. Use connections for anything new.

**How the agent uses it.** Each connection is an AWS profile of the same name inside the box:

```sh
aws s3 ls --profile staging-read
agentbox-env --list              # connections: name, label, prod, account, role, region, write-capable, description
aws configure list-profiles      # also lists them
```

```hcl
provider "aws" {
  profile = "staging-read"       # Terraform picks up the profile's region
}
```

- **No default with two or more connections.** A bare `aws` command fails with `agentbox: several AWS connections are bound — pick one with --profile <name> or AWS_PROFILE; run agentbox-env --list` (the AWS CLI adds its own error text in front). Terraform without a `profile` shows only a generic "No valid credential sources found"; run `agentbox-env --list` to see the names. With exactly one connection bound, `AWS_PROFILE` is set to it, so nothing changes for the agent.
- **Region.** `AWS_REGION` and `AWS_DEFAULT_REGION` are set only when every bound connection shares one region, because they would override each profile's own region. When regions differ they are left unset and each profile supplies its own. Some scripts read only `AWS_REGION`; pass `--region` (or set it for that one command) there.

**Write access.** A connection with `elevate` can be widened to its write role for a fixed window, but only **one connection per session at a time**:

- Granting a different connection while a window is open is refused: drop the first one.
- After you drop a window, the write credentials already issued stay valid until they expire (15 minutes at most). A *different* connection can be granted only after that; the refusal names the time. Granting the same connection again is allowed at once.
- A session that binds any write-capable connection is exclusive: it gets its own box, even if you never grant a window.
- A connection other than the elevated one stays on its own `role_arn` while a window is open, so it can write only if that role can (see the base-role check below).

The elevate block takes these optional limits, which the grant panel and the connection editor show:

| `elevate` field | Effect |
|---|---|
| `allow_iam` | Lets the window change IAM. The agent can then create and use its own roles, so the data-read blocks are advisory in that window. |
| `pass_role_prefix` | Roles the window may pass with `iam:PassRole`: only those named `<prefix>*`. Code running as such a role is not bound by the window's blocks (data reads, bucket policies and ACLs). Left out, no role can be passed, so Lambda, ECS and instance-profile applies fail. 3 to 40 role-name characters; `agentbox-`, `aws` and `service-role` stems are refused. |
| `box_bucket_prefix` | Objects in buckets named `<prefix>*` are readable in the window, and those buckets' policies, ACLs and public access blocks can change (config file only). Left out, the window's credentials cannot change any bucket's policy, ACL or public access block; code running as a role passed with `pass_role_prefix` is not bound by that. Shared stems such as `aws`, `cdk-` and `sagemaker-` are refused. |

Details and what each one weakens: [docs/aws/agent-role](aws/agent-role/README.md).

Read-only comes from the role, not from CuliBox: point each connection's `role_arn` at a read-only role and put write access only in `elevate.role_arn`. The session policy CuliBox adds narrows data reads; it does not remove writes, so a connection whose `role_arn` can write can write at any time.

**Base-role check.** At spawn, CuliBox asks each base role whether it can write. It runs IAM policy simulation of 11 broad write actions (`s3:PutObject`, `s3:DeleteObject`, `s3:CreateBucket`, `ec2:RunInstances`, `ec2:TerminateInstances`, `iam:CreateUser`, `iam:AttachRolePolicy`, `lambda:UpdateFunctionCode`, `rds:DeleteDBInstance`, `dynamodb:PutItem`, `cloudformation:CreateStack`) with the role's own credentials. The UI, `agentbox-env --list` and `culibox doctor` then show one of:

- **role can write**, with the allowed actions.
- **no broad writes found**.
- **unverified**, with a short reason: `SSO expired`, `assume-role denied`, `role cannot simulate itself`, `timed out`, `check failed`, `not checked` or `probe disabled`.

"No broad writes found" is not proof of read-only. A write grant scoped to one resource or behind a condition is not detected, and permission boundaries and SCPs count only as far as IAM simulation evaluates them.

The result is fixed at spawn and cached for up to 1 hour. Respawn to pick up a role change. An unverified result is checked again after the next successful base (non-elevated) credential mint for that connection, for example after an SSO sign-in. The exceptions are `role cannot simulate itself` and `assume-role denied`: a sign-in cannot fix those, so the check stops. Fix the role, then respawn. A custom role needs `iam:SimulatePrincipalPolicy` on itself (`ReadOnlyAccess` includes it). The check only warns. It never refuses a spawn. (A connection in `sso_direct` mode has no agent role; it gets the read-only gate below instead, which does refuse.)

### Limits and operations

- Connections made on the Credentials screen apply at once. Connections declared in `config.json` are read at daemon start only: edit the file and restart CuliBox. A running session keeps the definition it was spawned with until you kill it, including a connection you have since removed; saving or spawning a profile that names a removed connection is refused.
- Each connection signs in through its own `source_profile`. If connections use different SSO sessions and one expires, only that connection's credential requests fail, and the UI shows which source to sign in to.
- `culibox doctor` prints one `aws-connection <name>` line per connection: whether the `source_profile` exists in the host's AWS config (a missing one fails), whether its SSO login is valid or expired (checked once per SSO session, so connections sharing a login cost one check), and whether the connection can be elevated. It reads the shell's own AWS config, not the daemon's environment. A second line, `aws-connection <name> base role`, runs the base-role check above; inline `aws` bindings (from `config.json` and the profile store) get `aws-binding <profile>`. A base role that can write, or one that could not be checked, is a WARN; none found is a PASS. The check is never a FAIL.
- **The Sign-in column** on the Credentials screen reads the host's SSO token cache (`~/.aws/sso/cache`, in the profile's sso_session or start URL key; the AWS config comes from `AWS_CONFIG_FILE` when set), with no AWS call. **Signed in** (green, with the expiry) means a cached token that has not expired. **Sign-in expired** (amber) is an expired one that cannot be renewed; **Renews on use** is an expired session token the SDK can refresh, with a client registration that has not lapsed (whether the refresh still works is only known by asking AWS, so it is not green); **Not signed in** has no cache file; **Unknown** could not be read; **Not SSO** is a static-key or other non-SSO source profile (the AWS SDK's own order decides: static keys win over an sso_session; an `sso_direct` connection needs a pure SSO profile, so it shows **Not an SSO profile** otherwise); **SSO profile incomplete** has SSO settings that are missing (a start URL, region, account or role) or that do not match its sso-session (a different start URL or region); **Profile not found** is a source profile missing from the AWS config. Only the state and the expiry reach the browser, never a token. It is a local read, so a token revoked on the AWS side still shows as signed in until its expiry.
- **Rolling back to an older binary (operator step).** An older CuliBox ignores the `aws_connections` key, so a profile that uses it spawns **unbound**: no AWS credentials, and also no egress allowlist, so its box gets an open network like any unbound profile. Saving such a profile in the old UI drops the key permanently, and restored sessions get a fresh box. So before rolling back, kill every session that uses `aws_connections`, and do not edit those profiles in the old UI until you roll forward. Profiles with an older inline `aws` binding are unaffected.

## SSO role directly (opt-in)

The recommended way to give an agent AWS access is the **agent role**: a role you create (see [docs/aws/agent-role](aws/agent-role/README.md)), assumed for 15 minutes at a time with a session policy that narrows it, and recorded in CloudTrail as the agent session. That stays the default.

In some accounts you cannot create that role. For those, a connection can opt in to **`"mode": "sso_direct"`** (on the Credentials screen, "SSO role directly" next to "Agent role (recommended)" under "Credentials the agent gets"): the agent is given your SSO permission set's own credentials. It is meant for permission sets that are **read-only**, and CuliBox uses it only when the gate passes: a check of the permission set's own identity policies (see "What the gate does not see" below).

In `config.json` the same connection reads:

```json
"aws_connections": [
  { "name": "company-read", "label": "Company, read-only", "mode": "sso_direct",
    "source_profile": "company-sso", "region": "eu-west-1" }
]
```

The UI states what you give up:

> SSO role directly: the agent gets your permission set's own credentials, valid for up to N h. CloudTrail shows you, not the agent, and they cannot be revoked when the session ends. Read-only permission sets only.

- **No session policy, no S3-read scoping, no elevate.** Nothing narrows the permission set, and a write window cannot be granted in this mode.
- **Long-lived.** Credentials keep the expiry of your SSO role credentials (up to N hours), not 15 minutes, and cannot be revoked when the agent session ends.
- **No agent attribution.** CloudTrail shows you, not an `agentbox-*` session.
- **SSO profiles only.** The `source_profile` must be a plain IAM Identity Center profile. Static keys, a `credential_process` or an assume-role chain are refused, so no long-lived key is ever handed out.

**The read-only gate.** CuliBox reads the permission set's own inline and attached policies. Every Allow must be a read verb or a reviewed non-standard one, only an unconditional `Resource: "*"` Deny removes an Allow, and a screen over an embedded catalog of AWS actions, plus a reviewed list of secret-returning reads, requires credential-minting and secret-returning actions to be denied. An IAM policy simulation then confirms that writes, side-effecting reads (such as `sqs:ReceiveMessage`) and those actions are denied. The gate runs when you save, when a session spawns, in `culibox doctor`, and on **every** credential mint (a refresh, about 5 minutes before expiry, is a mint). A permission set that gains a write mid-session is refused at the next mint; credentials already issued keep working until they expire (up to N hours), under the permission set's current policy, because the gate only re-checks at a mint. Anything it cannot verify (SSO expired, a timeout, a missing IAM read permission) refuses; there is no override. The refusal lists every action that failed.

**`ReadOnlyAccess` alone is refused.** It allows `sqs:ReceiveMessage`, `ec2:GetPasswordData`, ECR and CodeArtifact authorization tokens and more, about 270 actions in all. Add an inline Deny to the same permission set that denies them unconditionally (no `Condition`, `Resource: "*"`); a tag- or principal-based exception is refused. The refusal message gives the list to write the guardrail from, and [docs/aws/agent-role](aws/agent-role/README.md#sso-direct-mode-read-only-permission-sets) has an example and the permission set's own IAM read requirements.

**What the gate does not see.** This is a check of the permission set's identity policies, not a guarantee.

- Resource-based policies (bucket, queue and key policies) are invisible: AWS cannot simulate them for roles.
- Denies that live only in an SCP or a permission boundary do not count.
- Secrets stored in configuration (Lambda environment variables, EC2 user data and launch templates, CloudFront origin headers, SSM parameters) are only reported by the data-read report, never blocked.
- The gate reads the source profile's settings from the host's AWS config, including `endpoint_url`. A local endpoint set on that profile answers the gate too.
- The list of reads that return secrets is kept by hand and is not complete. Some reads whose replies may carry a secret are deliberately not blocked, because a deny would remove a core read: Redshift and RDS `PendingModifiedValues.MasterUserPassword`, which AWS documents as a response field and which may be masked (not verified against live AWS), EC2 VPN `PreSharedKey`, and DMS and DataSync `Password`.
- Actions AWS adds after the catalog snapshot (2026-10-30) are unscreened under a wildcard the permission set already allows, until it is refreshed with `go run ./internal/awsprobe/gencatalog`.

Prefer the agent role whenever you can create it. The account guidance in [docs/aws/agent-role](aws/agent-role/README.md#which-accounts) applies to this mode too.
