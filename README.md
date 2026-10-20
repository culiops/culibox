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

- **Boxed runtime (the default).** Each project gets its own Docker container: only the project directory is mounted writable, with no host home, no `~/.ssh`, no Docker socket and no inherited daemon environment. The container runs as your uid, drops all capabilities, sets `no-new-privileges`, uses a read-only root filesystem, and has a PID limit and a memory cap (`box_memory`). Idle boxes are removed after a timeout. A `host` tier (no containment) exists for work that needs the machine, and you choose it explicitly.
- **macOS.** Boxes run in agent-box's own [colima](https://github.com/abiosoft/colima) VM, separate from any Docker setup you already have. See [docs/MACOS.md](docs/MACOS.md).
- **Config bundle.** A boxed session gets an allowlisted slice of your agent configuration (`CLAUDE.md`, skills, agents, commands, a filtered `settings.json`, and MCP servers that can run inside the image). Credentials, history and transcripts are never copied. Your Claude Code plugins come along as a read-only, git-free copy. If any part of the plugin set is a symlink, or a marketplace URL in it carries credentials, the whole set is left out and recorded as a drop. The box image can declare its own MCP servers in `/etc/agent-box/mcp.json`; they replace same-named servers from your config, and each replacement is recorded as a drop. The image ships Chromium and the Playwright MCP server (`playwright`), which writes its output to `/tmp/playwright-mcp`.
- **Multiple accounts.** Run sessions under named agent accounts, each with its own config directory and its own token, kept apart from your personal login. Only Claude accounts can launch sessions.

### Cloud access, audit and secrets

- **AWS credential broker.** A boxed profile can carry an `aws` binding. The host mints 15-minute credentials by assuming a read-only IAM role from your own SSO session, with an inline session policy that narrows the role further (see [Security model](#security-model)). The box never sees your SSO session. Credentials reach the AWS CLI and Terraform inside the box through `credential_process`.
- **GitHub access.** A boxed profile can carry a `github` binding. The broker mints one-hour, read-only GitHub App installation tokens scoped to the repos you list; `git` and `gh` in the box use them, and the box never holds a personal access token (see [GitHub access for agents](#github-access-for-agents)).
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

**Pull instead of build.** Set `"box_image": "ghcr.io/culiops/agent-box@sha256:<digest>"` (the digest is in the CI job summary, or from `docker buildx imagetools inspect ghcr.io/culiops/agent-box:base`) and run `agent-box box setup`: it pulls the image for your daemon's architecture instead of building it. Pin by digest, never by `:base`.

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
| `box_image` | `culiops/agent-box:base` | Image used for boxes. Set it to a dated tag to pin an instance; agent-box never rebuilds or pulls a pinned tag. A digest or registry ref is pulled by `box setup` and never built; a box spawn never pulls. |
| `box_idle_ttl_seconds` | `900` | How long a box outlives its last session. |
| `box_pids_limit` | `512` | Max processes and threads per box. |
| `box_tmp_size` | `512m` | Size of the box's `/tmp` tmpfs; number + k/m/g. |
| `box_memory` | `4g` | RAM cap per box; number + k/m/g, at least `1g`. Swap is capped to the same value, so a box over its cap is killed rather than swapping the host. |
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
| `github_apps` | none | GitHub App connections the credential broker mints installation tokens with. Read at daemon start only: a change needs a restart. |
| `static_tokens` | none | Long-lived token entries (a Grafana, Sentry or Atlassian token, a GitHub personal access token) that a boxed profile can pick by name. See [Static tokens for agents](#static-tokens-for-agents). Read at daemon start only: a change needs a restart. |
| `env_label`, `env_color` | unset | Badge shown in the UI. |

`box_pids_limit`, `box_tmp_size` and `box_memory` apply to boxes created after a restart; a running box keeps its limits until it is recreated.

Each entry in `agents` takes `name` and `bin` (required), plus `args`, `base`, `env`, `secrets`, `options`, `runtime`, `default_cwd`, `account`, `aws`, `github` (see [GitHub access for agents](#github-access-for-agents)), `tokens` (see [Static tokens for agents](#static-tokens-for-agents)), `clone_of` and `clone_share` (see [Clone mode](#clone-mode)). `base` is the environment policy: `inherit` (the default; the daemon's environment minus Claude session markers), `clean` (a minimal environment with only what you grant by name) or `login` (run through `$SHELL -l`). Top-level `options` define the launch toggles. A profile only honours per-spawn overrides for options it lists; `POST /api/sessions` reports any it ignored in an `options_dropped` list of `{key, reason}` in the 201 body. Images are instance-wide: set `box_image`. A profile carrying `box.image` is refused on save and ignored, with a log line, when loaded from an older store or config.

## Clone mode

Clone mode lets a boxed agent work on a private copy of one of your repos. Nothing the box writes reaches the original except through `agent-box clone fetch`, which brings a branch home for you to review. Linux only for now.

Two profile fields turn it on:

| Field | Meaning |
|---|---|
| `clone_of` | Absolute path of the host repo to copy. It must be a plain git repo with a `.git` directory (no worktree, no `commondir`, no `objects/info/alternates`), not a symlink, and not your home directory or any directory above it. |
| `clone_share` | Up to 8 paths, relative to `clone_of`, that the box can read. Example: `["CLAUDE.md", "docs/notes"]`. |

The profile's `default_cwd` is the clone's directory. The two must not overlap: neither may be inside the other. The profile must be boxed: `runtime: "host"` is refused, and so is a profile that sets no runtime while the instance's `default_runtime` is `host`, both when you save it and when you spawn it. The first spawn creates the clone with host `git clone`. Spawn the session in the clone's root, not a subdirectory.

**What the box gets**

| In the box | Mode | What it is |
|---|---|---|
| the clone directory | read-write | The box's own copy of the repo, and the workspace. |
| the clone's `.git` | read-write | Part of that copy. The box commits here. |
| `<clone_of>/.git` | read-only | The original's history, so `git fetch origin` inside the box works. The box cannot change it. |
| each `clone_share` path | read-only | The original's file, shown at the same path inside the clone. |
| the host's Claude memory for `clone_of`, if any | read-only | So the boxed agent starts with what you already told Claude about this repo. |

Claude's memory notes for `clone_of` are readable by the box, which has internet egress. Keep secrets out of them, or set `bundle_source: "off"`, which also turns the memory mount off.

Everything else is as in any boxed session: no host home, no Docker socket, egress only through the proxy.

**Shares are read-only, and readable.** There is no writable share: a `:rw` suffix is refused by name, and so is any other `:`. A share path must be relative and clean, cannot be `.git`, cannot nest inside another share, and no part of its path may be a symlink. The box can read everything you share and has internet egress, so never share secrets. agent-box warns for names like `.env*`, `*.pem`, `*.key` and `config*.json`, but it does not stop you. A shared `CLAUDE.md` is readable in the box but is **not** loaded as project instructions: boxed Claude runs with `--setting-sources user`. Tell the agent to read it first, or put box guidance in your user-level `CLAUDE.md`. Files the box writes for you (plans, notes) go in a directory inside the clone, such as `docs/handover/box/`, that your `.gitignore` excludes; they never travel in a fetch.

**Bringing code home**

```sh
agent-box clone fetch [-config <path>] [-base <branch>] <profile> <branch>
```

The flags come before the two names. `-base` defaults to `main`. agent-box makes a git bundle of `<branch>` from the clone inside a throwaway container with no network and the clone mounted read-only, then verifies it and fetches it into `clone_of` as `box/<branch>`, with git's object checks on. The host never runs git against the clone's own `.git`, and never merges: the branch just appears. The command prints the commits that came, a warning for any commit message that carries an AI-attribution trailer, and the review line:

```text
fetched box/feat-x (2 commit(s) not on main):
  abc1234 add the thing
  def5678 fix the thing
review: git -C /home/me/repo log -p main..box/feat-x; merge per your review policy
```

Commit subjects are printed with control characters replaced, because a box wrote them. Bundles over 512 MiB are refused. The container runs the global `box_image`.

**Refusals**

- A host-tier session is refused anywhere inside a clone, including a subdirectory or a path that does not exist yet. The box wrote those files, and host-tier tools would trust them. A host-tier session at `clone_of` or at a share source is fine.
- agent-box records every directory it has used as a clone in `<data dir>/clone-dirs`, one path per line. A recorded directory stays box-owned even after its profile is edited or deleted. Deleting the directory does not release it. To release one, remove its line from `<data dir>/clone-dirs`. An unreadable record refuses every host-tier spawn until it is fixed.
- A spawn is refused, never partly started, if `clone_of`, a share source or a mountpoint is unsafe: a symlink on the path, a path inside the data root or a config directory, or an existing clone whose `origin` is not `clone_of`.
- A `clone_of` that is inside another profile's clone, or inside a recorded clone directory, is refused when you save the profile, when you spawn it and in `clone fetch`: the owning box could have planted git hooks there.
- Changing `clone_of`, `default_cwd` or `clone_share` gives the profile a new box rather than reusing the old one.

**Known limits**

- Never put `clone_of` inside a boxed workspace (or any other directory a box can write): a box could plant git hooks in that repo, and the host runs git there during `clone fetch`. agent-box refuses a `clone_of` that is inside a clone or a recorded clone directory, and runs its own git calls with hooks disabled, but it cannot see every directory a box can write.
- Never run a host-tier agent session in a directory above the clones (for example your home). Claude Code lazily loads nested `CLAUDE.md` and `.claude/` files from any subdirectory it reads, so files a box wrote under a clone could steer a host agent. Keep clones outside every tree where host sessions run.
- Your own shell inside a clone cannot be policed. A box can plant git hooks, config and aliases in the clone's `.git`, so never run git or other host tools there. Review through `clone_of` after a fetch, not in the clone.
- A file you copy out of a clone by hand might be a symlink the box planted, and `cp` would copy its target. Check that it is a regular file first.
- The box and the host share a disk. A box can fill it; there is no per-box quota.
- agent-box's file editor follows links inside box-written directories. Treat a clone's contents as untrusted when you open them in the UI.

## GitHub access for agents

A boxed profile can carry a `github` binding. The credential broker then mints short-lived, read-only GitHub App installation tokens for the repositories you list, so the agent can `git clone`, `git fetch` and use `gh` without a personal access token in the box. Linux only for now: a bound profile is refused on macOS and on the host tier.

**1. Create the GitHub App.** Give it read permissions only (agent-box never asks for write). Make it public ("Any account") if you will install it on more than its owner account; otherwise create it under the org that owns the repos. Install it on each owner you want to reach and select the repositories. Generate a private key and note the App's client id.

**2. Store the private key.** Put the PEM file where the daemon user owns it, as a regular file (not a symlink) with mode `0600` or tighter. Then declare the App in `config.json` and restart agent-box (see the `github_apps` row in [Configuration](#configuration)):

```json
"github_apps": [
  { "name": "my-org", "client_id": "Iv23liXXXXXXXXXXXXXX",
    "private_key": { "type": "file", "path": "/home/me/.config/agent-box/my-org.pem" } }
]
```

The key is never copied into a box. agent-box reads the key file once per daemon run and caches it after the first load, so after you rotate or change the key file, restart agent-box. When a bound session spawns, agent-box checks that the App is configured and its key loads (no network call), and refuses the spawn if not.

**3. Add the binding to a profile.** Set `"runtime": "box"` explicitly, then fill in the GitHub section of the profile form, or write it in `config.json`:

```json
"github": {
  "owners": [
    { "owner": "my-org", "app": "my-org", "repos": ["api", "web"] }
  ]
}
```

| Field | Meaning |
|---|---|
| `owners[].owner` | The account or organisation that owns the repos. |
| `owners[].app` | The `name` of a `github_apps` entry that is installed on that owner. |
| `owners[].repos` | The repositories a token may reach (up to 100 per owner). |
| `permissions` | Optional, read-only. The default is `actions`, `checks`, `contents`, `pull_requests`, `issues` and `metadata`; `statuses` can be added. Any value other than `"read"` is refused. |
| `egress_allow` | Optional extra hosts for this binding's boxes, on top of the GitHub hosts below. |

The box image must include `gh`: use an image built from this version (tag `2026-10-19.2` or later) and set `box_image` to it (see `box setup` above).

**How the agent uses it**

- `git` over HTTPS to `github.com` asks the broker for a token through a credential helper. Nothing is stored and no token is in the environment.
- `gh` is a thin wrapper that fetches a token for the repo's owner and sets `GH_TOKEN` only on the real `gh` process it starts. In a clone-mode box, `GH_REPO` is set from the host repo's `origin` when that repo is in the binding, because the box's own `origin` is a host path.
- Each token lasts one hour (GitHub's fixed lifetime for installation tokens), covers only the repos you listed and the read permissions you chose, and is reused until 10 minutes before it expires.
- Two sessions with an identical binding share one box and one uid, so their audit rows are effectively per box. That is no scope escalation: both hold the same scope.
- Every mint is written to the audit log (`audit.db`) with the session, owner and installation, never the token. If that row cannot be written, no token is returned.
- Tokens are revoked at GitHub when the agent exits or you kill the session, and on a clean daemon shutdown. If the daemon crashes, they are left to expire, at most one hour later.

**Egress.** A GitHub-bound box may also reach `api.github.com`, `codeload.github.com`, `results-receiver.actions.githubusercontent.com` and `productionresultssa<N>.blob.core.windows.net` (the Actions log shards `productionresultssa0` to `productionresultssa199` except 22, never `*.blob.core.windows.net`), plus the binding's `egress_allow`. `github.com` is already in the default allowlist.

**What this does and does not protect against**

- **The agent can read the token while it runs.** It can run `git credential fill`, read the `gh` child's environment, or call the broker socket itself. The helper design prevents accidental exposure only: the token is not inherited by every child process or MCP server, does not show up in `env` dumps and is not pasted into transcripts. It does not stop a deliberate read.
- The real controls are the one-hour lifetime, the repo and read-only scope, the egress lock, the per-mint audit and revocation when the session ends.
- **Residual: exfiltration through `github.com`.** It is open to every bound box, so an agent could push data to a repository it controls, using credentials it brought itself. This is not new and not fixed here.

## Static tokens for agents

Some services have no short-lived credential to mint: Grafana service-account tokens, Sentry auth tokens, Atlassian API tokens, a GitHub personal access token. A boxed profile can bind **static token entries** that you define once in `config.json`. The agent gets a token only for one command at a time, through a small in-box wrapper, and the box's egress is opened to exactly the hosts the entry names. Linux only for now: a bound spawn is refused on macOS until agent-box's egress control lands there, and on the host tier.

**What this does and does not protect against**

- **The agent can read a bound token.** It can run `agentbox-env grafana-prod -- env`, read the child's environment, or call the broker socket itself.
- What it prevents is accidental exposure: the token is not inherited by every process and MCP server in the box, does not show up in `env` dumps and is not pasted into transcripts. You also get an audit row per fetch, an egress lock and scoping to the hosts you list.
- It does not stop a deliberate read, and the token outlives the session. The UI says so next to the picker: *"The agent can read this token. It is long-lived and is not revoked when the session ends."*
- **Egress is per session, not per process.** Once an entry is bound, every process in the box can reach that entry's hosts, not only the `agentbox-env` child.

**1. Define the entry.** Put the token in a file owned by the agent-box user, a regular file (not a symlink) with mode `0600` or tighter, or in the daemon's own environment. Then declare the entry in `config.json` and restart agent-box:

```json
"static_tokens": [
  { "name": "grafana-prod", "provider": "grafana", "label": "Grafana", "prod": true,
    "value": { "type": "file", "path": "/etc/agent-box/tokens/grafana-prod" },
    "env": ["GRAFANA_SERVICE_ACCOUNT_TOKEN"],
    "config_env": { "GRAFANA_URL": "https://grafana.example.com" },
    "egress": [ { "host": "grafana.example.com" } ],
    "mcp": { "server": "grafana-prod", "command": "mcp-grafana" } }
]
```

| Field | Rules |
|---|---|
| `name` | Required, unique. `^[a-z0-9][a-z0-9-]{0,62}$`. Profiles refer to the entry by it. |
| `value` | Required. Where the token lives: `{"type": "file", "path": "<absolute path>"}` (0600, owned by the agent-box user, not a symlink) or `{"type": "env", "var": "<NAME>"}` (the daemon's own environment; a name starting with `AB_`, or the `password_env` variable, is refused). Any other type is refused with `unsupported reference type "..." (want "file" or "env")`. One trailing newline is trimmed; an empty value or one with a control character is refused. The reference is read at each fetch and never cached. |
| `env` | Required, 1 to 4 variable names (`^[A-Za-z_][A-Za-z0-9_]*$`) that carry the token. Names that change how programs run or connect (`PATH`, `HOME`, `NODE_OPTIONS`, `LD_*`, `*_proxy`, `npm_config_*`, `GIT_SSH*`, `AWS_*` credential variables, `AGENTBOX_*`, `AB_*` and similar) are refused. Names that only carry a token, such as `GH_TOKEN`, are allowed. |
| `config_env` | Optional, up to 16 `NAME: value` pairs set next to the token (a URL, an org). Values up to 1024 bytes, no control characters, no name also in `env`. **Not secret**: it shows in the UI, in `agentbox-env --list` and possibly in audit rows. Never put a secret here. |
| `egress` | Up to 16 hosts the entry's boxes may reach. Each is an exact lowercase DNS name: no `*.` wildcard (a shared domain like `*.atlassian.net` would open every tenant) and no IP address. See [self-hosted services](#self-hosted-services) for `port`, `private` and `cidr`. An entry with no `egress` can only serve as a GitHub PAT owner. |
| `mcp` | Optional. A box-side MCP server that uses this token: `server` (`^[a-z0-9][a-z0-9_-]{0,62}$`, unique across entries), `command`, `args` (up to 32, none containing `${`) and `extra` (up to 4 other entries the server also reads; each must be bound to the same profile). |
| `provider`, `label`, `description` | Optional, for the UI and `--list`. `provider` is `^[a-z0-9][a-z0-9-]{0,31}$` and groups the picker. `label` is at most 32 characters, `description` at most 200. |
| `prod` | Optional. Shows a "production" chip in the picker and a `[prod]` mark in `--list`. A label only: it changes no behaviour. |

**2. Bind it to a profile.** Set `"runtime": "box"` explicitly, then tick the entries in the profile form's **Tokens** block, or write the names in `config.json`:

```json
{ "name": "claude-ops", "bin": "claude", "runtime": "box", "tokens": ["grafana-prod"] }
```

A profile binds at most 16 entries, with no duplicates, and each must be defined. A name that would set a variable the profile already sets (in `env`, `secrets` or an option's env) is refused. A bound session's box is keyed by its token entries, so changing an entry gives the profile a new box.

**How the agent uses it**

```sh
agentbox-env grafana-prod -- sh -c 'curl -H "Authorization: Bearer $GRAFANA_SERVICE_ACCOUNT_TOKEN" ...'
agentbox-env NAME [NAME...] -- CMD [ARGS...]   # run CMD with those tokens in its environment only
agentbox-env --list                            # what is bound; never a value
```

`agentbox-env` asks the broker for each named token, then runs `CMD` (no shell, so a `$VAR` you type is expanded by your own shell before the token exists; wrap such a command in `sh -c '...'`) with the token and the entry's `config_env` in that one process's environment. Nothing is fetched until it runs; if any fetch fails, or two named entries would set the same variable, nothing runs. `--list` reads `/opt/agentbox/tokens.json`, which holds names, labels, hosts and `config_env` but no token.

One box can hold several environments of the same service. Give each its own entry, with distinct `mcp.server` names, and bind both:

```json
{ "name": "sentry-staging", "provider": "sentry", "value": { "type": "file", "path": "/etc/agent-box/tokens/sentry-staging" },
  "env": ["SENTRY_ACCESS_TOKEN"], "egress": [ { "host": "sentry.io" } ],
  "mcp": { "server": "sentry-staging", "command": "sentry-mcp" } },
{ "name": "sentry-prod", "provider": "sentry", "prod": true, "value": { "type": "file", "path": "/etc/agent-box/tokens/sentry-prod" },
  "env": ["SENTRY_ACCESS_TOKEN"], "egress": [ { "host": "sentry.io" } ],
  "mcp": { "server": "sentry-prod", "command": "sentry-mcp" } }
```

The two share a variable name, which is fine: the agent picks one per command (`agentbox-env sentry-prod -- ...`). Naming both in one command is refused.

**MCP servers.** An entry's `mcp` adds a server to a per-session MCP config. The server is started through `agentbox-env`, so it gets its token only for its own process. The merge order is image servers, then entry servers, then your host servers: the first one with a given name wins and the others are listed as dropped. Your host MCP config is not modified, and host servers that need tokens are still left out of boxes. A session's config that carries your personal servers is mounted read-only at `/opt/agent-session-mcp` and only into boxes of an account that inherits your personal config. Accounts that do not inherit it get the entry and image servers only. An entry is also left out, with a reason, when the box image lacks `agentbox-env` or the server's command.

**Self-hosted services.** Boxes reach the network only through the egress proxy, on port 443 and public addresses. Two optional fields on an `egress` item relax that for one exact host:

| Field | Meaning |
|---|---|
| `port` | A TLS port other than 443 (1 to 65535; unset or 0 means 443). The proxy only tunnels with `CONNECT`, so this is for TLS services. There is no plain HTTP. |
| `private` and `cidr` | Allow this host to resolve to a private address. They go together, and `cidr` must lie inside `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` or `fc00::/7` (and not wider than that range; for IPv6 at least `/32`). `100.64.0.0/10` is refused because it holds Tailscale. The box's own subnet, the host's addresses and loopback are never reachable, whatever `cidr` says. Each tunnel opened through an exception is audited as `allowed_private`. |

```json
"egress": [ { "host": "grafana.corp.example", "port": 8443, "private": true, "cidr": "10.20.0.0/24" } ]
```

**Residual:** an exception trusts DNS for that one name. Whoever controls it can point it at any address inside `cidr`, so make `cidr` as narrow as the service. `agent-box doctor` warns on a range wider than `/16` (IPv4) or `/48` (IPv6). The proxy dials from the host, so a VPN route on the host applies to these hosts.

**GitHub personal access tokens.** A `github` owner can name a token entry instead of an App: set `"token": "<entry>"` and leave `app` out (exactly one of the two). The entry's `value` is the PAT. It works for `git` and `gh` like an App, with these differences:

- A PAT is not narrowed by agent-box. The binding's `permissions` do not apply, and the token may reach more than the listed repositories. `repos` is only checked by the broker: a request has to name one of them, and one that names no repository is refused with `name the repository: a personal access token is not scoped by agent-box`. The `gh` wrapper fails closed when it cannot read the repository from `-R`.
- Nothing is revoked when the session ends: agent-box cannot revoke a PAT.
- Each fetch counts against the same 60-per-minute limit as `agentbox-env` (App tokens keep their own 10 per minute) and writes an audit row.
- The profile form's owner picker offers PAT entries in a "Personal access tokens" group and shows the same caveat.

**Audit.** Every fetch writes a row to `audit.db` (session, profile, entry name, outcome) before the value leaves, never the value; if the row cannot be written, no token is returned. That is one row per `agentbox-env` call and per `git` or `gh` call with a PAT owner. There is no pruning yet, so the table grows with use; plan for it.

**Limits and operations**

- A running session keeps the entry it was spawned with, even across a daemon restart. Deleting an entry stops its fetches but not its egress; kill the session to cut an agent off.
- `static_tokens` is read at daemon start only: adding, removing or editing an entry needs a restart. The token itself is read at each fetch, so rotating a token file takes effect with no restart. An `env` reference is the daemon's environment, so changing it needs a restart too.
- A session may fetch at most 60 tokens a minute; more are refused with a rate-limit error.
- `agent-box doctor` prints a line per entry: whether its reference resolves (a file problem fails; an `env` variable unset in doctor's own shell is a warning, because the daemon reads its own environment) and a warning for a wide private `cidr`.
- The box image must ship `agentbox-env` and the MCP server binaries your entries use (Grafana, Sentry, Atlassian): use an image built from this release and set `box_image` to it.

## Security model

agent-box starts shells and agents on your machine, so authentication is never optional.

**What is protected, and how**

- **Login.** Either a generated token (printed once in a URL, kept at `0600` in the data root) or a password from the env var named by `password_env`. Failed logins are throttled. Browsers get a signed 30-day cookie; delete `cookie.secret` to log everyone out. Named, revocable bearer tokens exist for programmatic clients and are stored as hashes.
- **Network exposure.** The default bind is loopback. Binding all interfaces is refused unless `allow_public` is set. Requests whose `Host` header is not in the allowlist are refused (DNS-rebinding defence), and state-changing requests are checked for same-origin.
- **Containment.** The boxed tier described above. Roughly, containment comes from what is absent inside the box. The hardening flags are the smaller part. A box cannot reach services on the host's loopback; on macOS agent-box checks this on every new box and refuses the box if the check fails.
- **Cloud access.** The broker mints credentials only for live boxed sessions, with a per-spawn token, rate limits, and a 15-minute lifetime. The role is capped by `ReadOnlyAccess`, and a generated session policy denies reading S3 objects (outside an allowlist you set per binding), Secrets Manager values, SSM parameters, DynamoDB items and KMS decrypt. Trust is limited to the SSO permission sets you name and requires a `SourceIdentity` of `agentbox-*`. Create the role with [docs/aws/agent-role](docs/aws/agent-role/README.md).
- **Static tokens.** A bound token is fetched per command through the broker, is audited, and reaches only the hosts its entry names. The agent can still read it, and it is long-lived and not revoked when the session ends. See [Static tokens for agents](#static-tokens-for-agents).

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
