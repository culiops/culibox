# agent-box

A self-hosted web control plane for running, watching, containing and steering AI coding-agent sessions.

agent-box is the safe way to let an AI agent touch your cloud. You start Claude Code sessions from a browser, each one in its own container by default, and you keep an eye on what they are doing and what they cost. AWS access, when you grant it, is short-lived and scoped, and every grant is logged.

## Why it exists

Letting an agent run commands against real infrastructure is useful and risky. The usual setup is a terminal on a machine that holds your SSH keys, your cloud credentials and your whole home directory, with the agent running as you.

agent-box puts a boundary around that. Sessions run in a per-project container that sees only the project folder. Cloud credentials are minted per session, last 15 minutes and are narrower than your own. You see every session in one place, including which ones are waiting for you.

It is built first for ops, cloud, DevOps and SRE work, where you want a control panel rather than an editor.

**What it is not:** an IDE or a code editor. There is a small file browser and editor for quick fixes, and it is kept deliberately thin. If you want to edit code all day, use an editor.

## Features

### Watch

- **Multi-session web terminal.** Run several agent sessions side by side in the browser (xterm.js over WebSocket), on desktop or phone.
- **Attention awareness.** Sessions report whether they are working, idle or waiting, and why: a pending tool call or permission prompt, or simply your turn. The tab title and favicon change while something needs you. For Claude Code sessions, a card shows the pending tool call.
- **Usage and cost.** Per-session context size and model, and account-level 5-hour and 7-day rate-limit windows when a statusLine hook provides them. A local analytics store (SQLite) ingests Claude Code transcripts into daily token usage, estimated cost per model, project and session, and skill/MCP attribution. Dollar figures are estimates from a built-in price table. Analytics can be turned off.
- **Environment badge.** `env_label` and `env_color` mark an instance as DEV, PROD and so on in the UI.

### Run and steer

- **Session persistence.** Agents run under [abduco](https://www.brain-dump.org/projects/abduco/), so they survive a restart of the agent-box daemon. On restart, agent-box re-attaches. Closing the browser or the terminal that runs `agent-box serve` does not kill sessions.
- **Steer from the UI.** Interrupt a session, restart an exited one (Claude Code sessions on the host tier resume the same conversation), and, if `"replies_enabled": true` is set in config.json (off by default; restart to apply), answer permission prompts or send a one-line reply from the needs-you card. This is limited to Claude Code sessions.
- **Profiles.** Agents are defined in the config file or created at runtime in the UI, with launch options (flags and env vars) as toggles on the spawn form. A quick-shell profile is detected from `$SHELL`.
- **Files.** Browse, search, view, edit, upload, and download folders as an archive, with size and file-count caps.

### Contain

- **Boxed runtime (the default).** Each project gets its own Docker container: only the project directory is mounted writable, with no host home, no `~/.ssh`, no Docker socket and no inherited daemon environment. The container runs as your uid, drops all capabilities, sets `no-new-privileges`, uses a read-only root filesystem and has a PID limit. Idle boxes are removed after a timeout. A `host` tier (no containment) exists for work that needs the machine, and you choose it explicitly.
- **macOS.** Boxes run in agent-box's own [colima](https://github.com/abiosoft/colima) VM, separate from any Docker setup you already have. See [docs/MACOS.md](docs/MACOS.md).
- **Config bundle.** A boxed session gets an allowlisted slice of your agent configuration (`CLAUDE.md`, skills, agents, commands, a filtered `settings.json`, and MCP servers that can run inside the image). Credentials, history and transcripts are never copied. The box image can declare its own MCP servers in `/etc/agent-box/mcp.json`; they replace same-named servers from your config, and each replacement is recorded as a drop.
- **Multiple accounts.** Run sessions under named agent accounts, each with its own config directory and its own token, kept apart from your personal login. Only Claude accounts can launch sessions.

### Cloud access, audit and secrets

- **AWS credential broker.** A boxed profile can carry an `aws` binding. The host mints 15-minute credentials by assuming a read-only IAM role from your own SSO session, with an inline session policy that narrows the role further (see [Security model](#security-model)). The box never sees your SSO session. Credentials reach the AWS CLI and Terraform inside the box through `credential_process`.
- **Attribution and audit.** Credentials are minted with `SourceIdentity` and role session name `agentbox-<session id>`, so CloudTrail records the agent session rather than you. The broker also writes every mint, refusal and failure to a local SQLite audit log, with no credential values in it.
- **Secrets by reference.** Config names secrets rather than containing them. Values are resolved at spawn time from the environment or a file, injected only into the agent that declares them, and never logged.
- **`agent-box doctor`.** Checks the machine and prints PASS, WARN or FAIL lines. It is read-only and prints no secrets, so the output is safe to paste into an issue.

## Requirements

- Go 1.26.5 or newer (see `go.mod`) to build.
- Linux or macOS. macOS needs 13+ on Apple Silicon (Intel is untested) for the boxed tier.
- [abduco](https://www.brain-dump.org/projects/abduco/) on `PATH`. It is required; the daemon will not start without it.
- For the boxed tier: Docker on Linux, or `colima` and `docker` on macOS.
- The agent CLI you want to run. Claude Code (`claude`) is the supported agent. Other terminal programs can be configured as agents (a shell, for example), but status, usage and reply features are Claude Code only.
- Node.js, only to run the browser-side tests in development.

## Quick start

Build:

```sh
git clone https://github.com/culiops/agent-box.git
cd agent-box
go build -o agent-box ./cmd/agent-box
```

Check the machine:

```sh
./agent-box doctor
```

With no config file, agent-box listens on `127.0.0.1:8899`, runs one `claude` agent on the **host** tier (not contained) and uses a generated login token. To try that:

```sh
./agent-box serve
```

It prints a login URL containing a one-time token. Open it in a browser on the same machine. The token is kept in `login.token` in the data root.

For the contained default, build the box image and write a config. On Linux:

```sh
./agent-box box setup      # builds the box image with your local Docker
```

`box setup` tags every build with its date (for example `culiops/agent-box:2026-10-18`) and prints that tag and how to pin it.

On macOS, `box setup` also creates the VM; see [docs/MACOS.md](docs/MACOS.md).

Put this in `~/.local/share/agent-box/config.json`:

```json
{
  "bind_addr": "127.0.0.1:8899",
  "default_cwd": "/path/to/projects",
  "default_runtime": "box",
  "agents": [
    { "name": "claude", "bin": "claude" }
  ]
}
```

Then:

```sh
./agent-box serve
```

Open `http://127.0.0.1:8899` (use the printed login URL the first time). Sessions are boxed unless a profile says `"runtime": "host"`. The workspace folder must not be your home directory, because a box mounts the whole workspace. See [config.example.json](config.example.json) for a fuller example.

Claude Code needs a login inside the box. Create a long-lived token with `claude setup-token`, store it as a secret (see `secret_sources`), and list `CLAUDE_CODE_OAUTH_TOKEN` under the agent's `secrets`.

## Configuration

Everything agent-box writes lives in one data root: `$AGENT_BOX_HOME` (an absolute path), else `~/.local/share/agent-box`. The config is read from `<data root>/config.json`, or from `-config <path>`. A `config.json` in the current directory is deliberately never read. The data root and config file must be owned by you and not writable by others.

| Key | Default | Meaning |
|---|---|---|
| `bind_addr` | `127.0.0.1:8899` | Listen address. Required when you write a config file. |
| `password_env` | unset | Name of an env var holding the login password. Unset means a generated login token. |
| `allow_public` | `false` | Must be `true` to bind all interfaces. Read the security section first. |
| `allowed_hosts` | `[]` | Extra `Host` values to answer to, such as a reverse proxy's hostname. |
| `default_cwd` | your home | Default workspace. Must not be your home when anything runs boxed. |
| `default_runtime` | `box` | Tier for agents that do not set their own: `box` or `host`. With no config file at all, the built-in default is `host`. |
| `box_image` | `culiops/agent-box:base` | Image used for boxes. Set it to a dated tag to pin an instance; agent-box never rebuilds or pulls a pinned tag. |
| `box_idle_ttl_seconds` | `900` | How long a box outlives its last session. |
| `box_pids_limit` | `512` | Max processes and threads per box. |
| `box_tmp_size` | `512m` | Size of the box's `/tmp` tmpfs; number + k/m/g. |
| `docker_bin` | `docker` | Docker CLI to use. |
| `bundle_source` | auto-detect | Agent config to bundle into boxes; `off` disables it. |
| `data_dir` | the data root | Where state lives. |
| `abduco_bin` | `abduco` | Path to abduco. |
| `secret_sources` | none | List of `{"type": "env"}` or `{"type": "file", "path": ...}`. |
| `max_upload_bytes` | 64 MiB | Per uploaded file. Zero or negative means the default, never unlimited. |
| `max_archive_bytes`, `max_archive_files` | 512 MiB, 20000 | Caps on folder downloads. |
| `analytics.enabled` | `true` | Token and cost analytics. |
| `box_vm_cpus`, `box_vm_memory_gib` | 4, 4 | macOS box VM size. |
| `broker_port` | `9071` | macOS only: credential broker port on `127.0.0.1`. |
| `env_label`, `env_color` | unset | Badge shown in the UI. |

`box_pids_limit` and `box_tmp_size` apply to boxes created after a restart; a running box keeps its limits until it is recreated.

Each entry in `agents` takes `name` and `bin` (required), plus `args`, `base`, `env`, `secrets`, `options`, `runtime`, `box.image`, `default_cwd`, `account` and `aws`. `base` is the environment policy: `inherit` (the default; the daemon's environment minus Claude session markers), `clean` (a minimal environment with only what you grant by name) or `login` (run through `$SHELL -l`). Top-level `options` define the launch toggles.

## Security model

agent-box starts shells and agents on your machine, so authentication is never optional.

**What is protected, and how**

- **Login.** Either a generated token (printed once in a URL, kept at `0600` in the data root) or a password from the env var named by `password_env`. Failed logins are throttled. Browsers get a signed 30-day cookie; delete `cookie.secret` to log everyone out. Named, revocable bearer tokens exist for programmatic clients and are stored as hashes.
- **Network exposure.** The default bind is loopback. Binding all interfaces is refused unless `allow_public` is set. Requests whose `Host` header is not in the allowlist are refused (DNS-rebinding defence), and state-changing requests are checked for same-origin.
- **Containment.** The boxed tier described above. Roughly, containment comes from what is absent inside the box. The hardening flags are the smaller part. A box cannot reach services on the host's loopback; on macOS agent-box checks this on every new box and refuses the box if the check fails.
- **Cloud access.** The broker mints credentials only for live boxed sessions, with a per-spawn token, rate limits, and a 15-minute lifetime. The role is capped by `ReadOnlyAccess`, and a generated session policy denies reading S3 objects (outside an allowlist you set per binding), Secrets Manager values, SSM parameters, DynamoDB items and KMS decrypt. Trust is limited to the SSO permission sets you name and requires a `SourceIdentity` of `agentbox-*`. Create the role with [docs/aws/agent-role](docs/aws/agent-role/README.md).

**Never bind agent-box to a public interface without a front door.** A reachable agent-box is a remote shell. If you need access from elsewhere, keep the bind on loopback or a private network and put an authenticating proxy or tunnel in front (add its hostname to `allowed_hosts`). Do not rely on the login page alone on the open internet, and use a long password.

**Known limits**

- The `host` tier is not contained. A host session is you, with an agent.
- The adversary in scope is an agent running arbitrary userspace commands in a box. A kernel-level escape is out of scope.
- On Linux, boxes reach the network only through agent-box's CONNECT proxy: port 443, hostnames only, never private addresses or addresses on the host's own interfaces. A public IP that is NAT'd to the host but not on one of its interfaces is not known to agent-box. AWS-bound boxes are limited to an allowlist. Boxes on the egress network cannot reach each other. macOS boxes still have normal outbound access.
- A box started in place sees the workspace root's `.git`, `.claude/` and any top-level `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md` or `.mcp.json` read-only, so it cannot commit there or edit them. Repos with submodules, or whose git config sets `core.worktree` or `core.bare` to anything but false, are refused. When `core.hooksPath` is nested (for example `.husky/_`), the whole top-level directory that holds it (`.husky/`) is read-only in the box.
- Those guards cover top-level names only, and block direct writes to them only. They do not make a workspace safe to hand back to the host:
  - The same names in subdirectories are writable: a box can edit an existing `sub/CLAUDE.md`, `sub/.mcp.json` or `sub/.claude/settings.json` (hooks), and a host session started in `sub/` loads them.
  - A box can create an absent `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md` or `.mcp.json`, and a new `.git` anywhere in the workspace, including its root when it is not a repo. Servers in a new `.mcp.json` still need Claude Code's project-MCP approval, unless you auto-approve project servers.
  - A read-only file can still run writable workspace code: a git hook that calls a repo script or reads a tool config such as `.pre-commit-config.yaml`, a `.claude` hook that runs `./scripts/…`, an `@import` in `CLAUDE.md`.
  - So after a boxed session, review changes and untracked files (`git status`, `git diff`) before you run host git or a host-tier session there.
- It is a single-user tool. There are no roles or per-user permissions.
- Sessions that share one box (same workspace and same AWS binding) can read each other's process environment, so attribution between them is best effort. Two sessions in the same folder share it read-write, so a session without the AWS binding can plant a file that a bound session then runs. Give a bound session its own workspace.
- Read access can still pull data into a transcript that `ReadOnlyAccess` allows and the session policy does not deny (Lambda code URLs, Athena and Glue results, CloudWatch Logs, and others). Use the broker with a sandbox or lab account for now, not production.
- The credential broker holds access to your SSO session. Treat the machine running agent-box accordingly.
- Persistence covers the daemon restarting, not the whole container or machine being destroyed. After a restart the terminal repaints, but earlier scrollback is not replayed.
- Codex accounts can be created and stored but cannot launch sessions yet.

## Platform notes

- [docs/DOCKER.md](docs/DOCKER.md): running agent-box itself in a container, and keeping sessions across restarts.
- [docs/MACOS.md](docs/MACOS.md): install, the colima-based boxed tier, rotating credentials.
- [docs/aws/agent-role](docs/aws/agent-role/README.md): the Terraform for the IAM role the credential broker assumes.

Only one agent-box can serve a data root at a time.

## Roadmap

- Egress allowlist for macOS boxes (Linux boxes have it).

## Development

```sh
scripts/test.sh
```

This runs `gofmt`, `go test ./...`, a macOS cross-build and `go vet`, the browser-side tests (`node --test`) and a lint of the page's script references.

## License

Apache-2.0. See [LICENSE](LICENSE).
