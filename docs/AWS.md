# AWS access

## Several AWS accounts in one session

A boxed profile with the inline `aws` binding reaches one AWS account. To give one session several (a staging account and a production account, say), define **named AWS connections** once in `config.json` and let profiles pick them by name. Each connection is a full binding of its own: its own source profile, role, region, session policy, egress hosts and optional write role. Like the other credential bindings, a bound spawn is refused on the host tier, and on macOS it goes through the egress allowlist (the allowlist is verified on a Mac; AWS credentials through the broker there are not yet; see [docs/MACOS.md](MACOS.md#every-box)).

**1. Define the connections.** Declare them next to `static_tokens` and restart CuliBox:

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

Each entry takes the fields of the inline `aws` binding (see [docs/aws/agent-role](aws/agent-role/README.md) for the roles) plus:

| Field | Rules |
|---|---|
| `name` | Required, unique. `^[a-z0-9][a-z0-9-]{0,62}$`. `default` and `agentbox` are reserved. Profiles refer to the connection by it, and it is the AWS profile name inside the box. |
| `description` | Optional, at most 200 characters, one line. Shown in the UI and in `agentbox-env --list`. |
| `region` | Required for every connection. |
| `mode` | Optional. Absent means the agent role. `"sso_direct"` uses the SSO permission set directly (see [SSO role directly](#sso-role-directly-opt-in)); then `role_arn`, `elevate` and `s3_read` must be left out. |

**2. Pick them in a profile.** Set `"runtime": "box"` explicitly, then tick the connections in the profile form, or write the names in `config.json`:

```json
{ "name": "claude-aws", "bin": "claude", "runtime": "box", "aws_connections": ["staging-read", "prod-read"] }
```

A profile binds at most 16 connections, with no duplicates, and each must be defined. A profile uses either the inline `aws` binding or `aws_connections`, never both; saving or spawning one that sets both, or that names an undefined connection, is refused.

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

**Write access.** A connection with `elevate` can be widened to its write role for a fixed window, as with the inline binding, but only **one connection per session at a time**:

- Granting a different connection while a window is open is refused: drop the first one.
- After you drop a window, the write credentials already issued stay valid until they expire (15 minutes at most). A *different* connection can be granted only after that; the refusal names the time. Granting the same connection again is allowed at once.
- A session that binds any write-capable connection is exclusive: it gets its own box, even if you never grant a window.
- A connection other than the elevated one stays on its own `role_arn` while a window is open, so it can write only if that role can (see the base-role check below).

Read-only comes from the role, not from CuliBox: point each connection's `role_arn` at a read-only role and put write access only in `elevate.role_arn`. The session policy CuliBox adds narrows data reads; it does not remove writes, so a connection whose `role_arn` can write can write at any time.

**Base-role check.** At spawn, CuliBox asks each base role whether it can write. It runs IAM policy simulation of 11 broad write actions (`s3:PutObject`, `s3:DeleteObject`, `s3:CreateBucket`, `ec2:RunInstances`, `ec2:TerminateInstances`, `iam:CreateUser`, `iam:AttachRolePolicy`, `lambda:UpdateFunctionCode`, `rds:DeleteDBInstance`, `dynamodb:PutItem`, `cloudformation:CreateStack`) with the role's own credentials. The UI, `agentbox-env --list` and `agent-box doctor` then show one of:

- **role can write**, with the allowed actions.
- **no broad writes found**.
- **unverified**, with a short reason: `SSO expired`, `assume-role denied`, `role cannot simulate itself`, `timed out`, `check failed`, `not checked` or `probe disabled`.

"No broad writes found" is not proof of read-only. A write grant scoped to one resource or behind a condition is not detected, and permission boundaries and SCPs count only as far as IAM simulation evaluates them.

The result is fixed at spawn and cached for up to 1 hour. Respawn to pick up a role change. An unverified result is checked again after the next successful base (non-elevated) credential mint for that connection, for example after an SSO sign-in. The exceptions are `role cannot simulate itself` and `assume-role denied`: a sign-in cannot fix those, so the check stops. Fix the role, then respawn. A custom role needs `iam:SimulatePrincipalPolicy` on itself (`ReadOnlyAccess` includes it). The check only warns. It never refuses a spawn. (A connection in `sso_direct` mode has no agent role; it gets the read-only gate below instead, which does refuse.)

### Limits and operations

- `aws_connections` is read at daemon start only: edit `config.json` and restart CuliBox. A running session keeps the definition it was spawned with until you kill it, including a connection you have since removed from `config.json`; saving or spawning a profile that names a removed connection is refused.
- Each connection signs in through its own `source_profile`. If connections use different SSO sessions and one expires, only that connection's credential requests fail, and the UI shows which source to sign in to.
- `agent-box doctor` prints one `aws-connection <name>` line per connection: whether the `source_profile` exists in the host's AWS config (a missing one fails), whether its SSO login is valid or expired (checked once per SSO session, so connections sharing a login cost one check), and whether the connection can be elevated. It reads the shell's own AWS config, not the daemon's environment. A second line, `aws-connection <name> base role`, runs the base-role check above; inline `aws` bindings (from `config.json` and the profile store) get `aws-binding <profile>`. A base role that can write, or one that could not be checked, is a WARN; none found is a PASS. The check is never a FAIL.
- **Rolling back to an older binary (operator step).** An older CuliBox ignores the `aws_connections` key, so a profile that uses it spawns **unbound**: no AWS credentials, and also no egress allowlist, so its box gets an open network like any unbound profile. Saving such a profile in the old UI drops the key permanently, and restored sessions get a fresh box. So before rolling back, kill every session that uses `aws_connections`, and do not edit those profiles in the old UI until you roll forward. Profiles with the inline `aws` binding are unaffected.

## SSO role directly (opt-in)

The recommended way to give an agent AWS access is the **agent role**: a role you create (see [docs/aws/agent-role](aws/agent-role/README.md)), assumed for 15 minutes at a time with a session policy that narrows it, and recorded in CloudTrail as the agent session. That stays the default.

In some accounts you cannot create that role. For those, a binding can opt in to **`"mode": "sso_direct"`** (in the credentials screen, "SSO role directly" next to "Agent role (recommended)"): the agent is given your SSO permission set's own credentials. It is meant for permission sets that are **read-only**, and CuliBox uses it only when the gate passes: a check of the permission set's own identity policies (see "What the gate does not see" below).

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

**The read-only gate.** CuliBox reads the permission set's own inline and attached policies. Every Allow must be a read verb or a reviewed non-standard one, only an unconditional `Resource: "*"` Deny removes an Allow, and a screen over an embedded catalog of AWS actions, plus a reviewed list of secret-returning reads, requires credential-minting and secret-returning actions to be denied. An IAM policy simulation then confirms that writes, side-effecting reads (such as `sqs:ReceiveMessage`) and those actions are denied. The gate runs when you save, when a session spawns, in `agent-box doctor`, and on **every** credential mint (a refresh, about 5 minutes before expiry, is a mint). A permission set that gains a write mid-session is refused at the next mint; credentials already issued keep working until they expire (up to N hours), under the permission set's current policy, because the gate only re-checks at a mint. Anything it cannot verify (SSO expired, a timeout, a missing IAM read permission) refuses; there is no override. The refusal lists every action that failed.

**`ReadOnlyAccess` alone is refused.** It allows `sqs:ReceiveMessage`, `ec2:GetPasswordData`, ECR and CodeArtifact authorization tokens and more, about 270 actions in all. Add an inline Deny to the same permission set that denies them unconditionally (no `Condition`, `Resource: "*"`); a tag- or principal-based exception is refused. The refusal message gives the list to write the guardrail from, and [docs/aws/agent-role](aws/agent-role/README.md#sso-direct-mode-read-only-permission-sets) has an example and the permission set's own IAM read requirements.

**What the gate does not see.** This is a check of the permission set's identity policies, not a guarantee.

- Resource-based policies (bucket, queue and key policies) are invisible: AWS cannot simulate them for roles.
- Denies that live only in an SCP or a permission boundary do not count.
- Secrets stored in configuration (Lambda environment variables, EC2 user data and launch templates, CloudFront origin headers, SSM parameters) are only reported by the data-read report, never blocked.
- The gate reads the source profile's settings from the host's AWS config, including `endpoint_url`. A local endpoint set on that profile answers the gate too.
- The list of reads that return secrets is kept by hand and is not complete. Some reads whose replies may carry a secret are deliberately not blocked, because a deny would remove a core read: Redshift and RDS `PendingModifiedValues.MasterUserPassword`, which AWS documents as a response field and which may be masked (not verified against live AWS), EC2 VPN `PreSharedKey`, and DMS and DataSync `Password`.
- Actions AWS adds after the catalog snapshot (2026-10-30) are unscreened under a wildcard the permission set already allows, until it is refreshed with `go run ./internal/awsprobe/gencatalog`.

Prefer the agent role whenever you can create it. The account guidance in [docs/aws/agent-role](aws/agent-role/README.md#which-accounts) applies to this mode too.
