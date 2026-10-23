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

- **AWS credential broker.** A boxed profile can carry an `aws` binding. The host mints 15-minute credentials by assuming a read-only IAM role from your own SSO session, with an inline session policy that narrows the role further (see [Security model](#security-model)). The box never sees your SSO session. Credentials reach the AWS CLI and Terraform inside the box through `credential_process`. A profile can instead bind several named AWS connections (see [Several AWS accounts in one session](#several-aws-accounts-in-one-session)).
- **GitHub access.** A boxed profile can carry a `github` binding. The broker mints one-hour, read-only GitHub App installation tokens scoped to the repos you list; `git` and `gh` in the box use them, and the box never holds a personal access token (see [GitHub access for agents](#github-access-for-agents)).
- **Attribution and audit.** Credentials are minted with `SourceIdentity` and role session name `agentbox-<session id>`, so CloudTrail records the agent session rather than you. The broker also writes every mint, refusal and failure to a local SQLite audit log, with no credential values in it.
- **Secrets by reference.** Config names secrets rather than containing them. Values are resolved at spawn time from the environment, a file or the encrypted store, injected only into the agent that declares them, and never logged.
- **Encrypted value store.** A small encrypted store in the data root holds tokens, a GitHub App key or the Claude token as ciphertext, so a disk snapshot or backup does not expose what you keep in it (see [Store](#store)).
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
| `secret_sources` | none | List of `{"type": "env"}`, `{"type": "file", "path": ...}` or `{"type": "store"}` (at most one). Tried in order, first hit wins. |
| `store.unlock_ttl` | `168h` | How long an unlocked [store](#store) stays unlocked (Linux). A Go duration, 1h to 720h. |
| `max_upload_bytes` | 64 MiB | Per uploaded file. Zero or negative means the default, never unlimited. |
| `max_archive_bytes`, `max_archive_files` | 512 MiB, 20000 | Caps on folder downloads. |
| `analytics.enabled` | `true` | Token and cost analytics. |
| `box_vm_cpus`, `box_vm_memory_gib` | 4, 4 | macOS box VM size. |
| `broker_port` | `9071` | macOS only: credential broker port on `127.0.0.1`. |
| `extra_ca_certs` | none | Absolute paths to CA certificate PEM files that boxes should trust, for a TLS-inspecting proxy. See [Behind a TLS-inspecting proxy](docs/MACOS.md#behind-a-tls-inspecting-proxy). Read at daemon start only: a change needs a restart. |
| `github_apps` | none | GitHub App connections the credential broker mints installation tokens with. Read at daemon start only: a change needs a restart. |
| `static_tokens` | none | Long-lived token entries (a Grafana, Sentry or Atlassian token, a GitHub personal access token) that a boxed profile can pick by name. See [Static tokens for agents](#static-tokens-for-agents). Read at daemon start only: a change needs a restart. |
| `aws_connections` | none | Named AWS connections (source profile, role, region, optional write role) that a boxed profile can pick by name, so one session reaches several accounts. See [Several AWS accounts in one session](#several-aws-accounts-in-one-session). Read at daemon start only: a change needs a restart. |
| `env_label`, `env_color` | unset | Badge shown in the UI. |

`box_pids_limit`, `box_tmp_size` and `box_memory` apply to boxes created after a restart; a running box keeps its limits until it is recreated.

Each entry in `agents` takes `name` and `bin` (required), plus `args`, `base`, `env`, `secrets`, `options`, `runtime`, `default_cwd`, `account`, `aws`, `aws_connections` (see [Several AWS accounts in one session](#several-aws-accounts-in-one-session)), `github` (see [GitHub access for agents](#github-access-for-agents)), `tokens` (see [Static tokens for agents](#static-tokens-for-agents)), `clone_of` and `clone_share` (see [Clone mode](#clone-mode)), `context_paths` (see [Reading other repos](#reading-other-repos-context_paths)). `base` is the environment policy: `inherit` (the default; the daemon's environment minus Claude session markers), `clean` (a minimal environment with only what you grant by name) or `login` (run through `$SHELL -l`). Top-level `options` define the launch toggles. A profile only honours per-spawn overrides for options it lists; `POST /api/sessions` reports any it ignored in an `options_dropped` list of `{key, reason}` in the 201 body. Images are instance-wide: set `box_image`. A profile carrying `box.image` is refused on save and ignored, with a log line, when loaded from an older store or config.

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

## Reading other repos (context_paths)

`context_paths` lets a boxed agent read files outside its workspace: a partner repo's `CLAUDE.md`, a docs folder, a project registry. Each listed path is mounted **read-only at the same absolute path** inside the box, so a path written in your own notes resolves unchanged there. Boxed profiles only, in-place or clone mode.

The box can read everything you list and has internet egress: never list secrets.

| Field | Meaning |
|---|---|
| `context_paths` | Up to 16 absolute, clean paths to directories or regular files. Prefer a directory to a single file (see Lifecycle). Example: `["/home/me/other-repo/docs", "/home/me/registry"]`. The profile editor shows it as "Readable host paths", one path per line, for boxed profiles only. |

The box gets the list in `AGENTBOX_CONTEXT_PATHS`, `:`-separated, so the agent (or your user-level `CLAUDE.md`) can discover what is readable. agent-box sets it itself: a value in the profile's `env` or the daemon's environment is dropped. As with a clone's `clone_share`, a listed `CLAUDE.md` is readable but **not** loaded as project instructions, because boxed Claude runs with `--setting-sources user`; tell the agent to read it.

**Refusals** (when you save the profile and when you spawn)

- A host-tier profile, including one with no runtime set while `default_runtime` is `host`: a host session can already read any path.
- More than 16 entries, a duplicate, or an entry inside another (list the outer one). A path that is not absolute or not clean, contains `:`, `,`, `"`, a control character or invalid UTF-8.
- `/`, your home directory or any directory above it, and paths that would shadow the box's own filesystem: `/etc`, `/usr`, `/var`, `/opt`, `/root`, `/run`, `/home/agent` and the other system directories, and anything inside them; and exactly `/home`, `/tmp`, `/mnt` and `/media`. A directory below those (`/tmp/x`, `/mnt/data/repo`) is fine, except agent-box's own in-box paths `/tmp/agentbox-gh` (and anything inside it) and any `/tmp/ac-session-*`.
- A path that does not exist, has a symlink in any component, or is not a directory or regular file.
- A path equal to or inside `~/.colima`, `~/.lima`, `~/.docker` or `~/.ssh`; equal to or inside your personal Claude config directory `~/.claude` (its hooks and settings run in host sessions, so it is protected); and agent-box's own data root, config and profile directories.
- A path inside a box-owned directory: a clone directory or one recorded in `<data dir>/clone-dirs` (see [Clone mode](#clone-mode)).
- A directory that contains a unix socket or a named pipe (FIFO) at any depth. A read-only mount does not stop the box from connecting to a socket or writing into a pipe: a Docker socket is root on the host, and a pipe feeds whatever host process reads it. The scan also refuses a directory with over 20000 entries or a subdirectory it cannot read, because either could hide one: list narrower subdirectories.
- A path that overlaps the profile's workspace (`default_cwd` or the clone directory) or `clone_of`, in either direction. Use `clone_share` to share part of a cloned repo.
- On macOS, a path outside the box VM's shared directories. Share its parent and run `agent-box box setup -recreate`.

**Warnings** (saved anyway; the save response and the profile editor show them)

- A path whose name looks like a secret or config file (`.env*`, `*.pem`, `*.key`, `config*.json`, `id_*`, `*.tfstate`, `*.tfstate.*`, `credentials*`, `*.tfvars`, `.netrc`, `.npmrc`, `.git-credentials`, `auth.json`, `*.p12`, `*.pfx`, `kubeconfig`).
- A directory with such files in it, scanned two levels deep (5000 entries, 20 names). `.gitignore` does not matter: the box reads whatever is on disk.
- A path that overlaps the workspace of another boxed in-place profile: that box can write what this one reads.

**Lifecycle**

- The list is part of the box's identity. Changing it gives the profile a new box, with a fresh box home, rather than reusing the old one. Boxed sessions started in the old box cannot be resumed from the new one, and Claude's first-run onboarding may run again.
- You cannot add a path to a running box. List partner repos up front, then start a session that uses the new list.
- A listed file is mounted by inode. Git checkouts and most editors replace a file by renaming a new one over it, and a running (or reused) box keeps seeing the old one. List the folder that holds it instead where you can; a changed file inside a listed folder is seen at once. On Linux a later spawn into a box whose file (or a re-created folder) no longer matches the path is refused by the identity check below until that box is recreated.
- The paths are checked when you save, again at spawn before the box is prepared, and once more right before it starts. On Linux that last check records each path's identity (device and inode, read without following any symlink), and once the box runs, and before the agent starts, agent-box compares it with what the box actually mounted. A mismatch means the path changed in between (for example a directory swapped for a symlink); the spawn is refused and a box created for it is removed. A box that other sessions still use is kept, and the refusal asks you to end those sessions so it is recreated.
- A socket or symlink that appears **after** the box starts is not caught: the mounts were made earlier and are not re-checked. Treat a listed directory as one that other tools on your machine can still change.
- Each spawn logs one line with the session id, the profile and the mounted paths.

**Known limits**

- On macOS the identity comparison is not made: inode numbers seen through the box VM's file share do not match the host's. A path that is swapped for a symlink between the last check and the box starting (for example by another box that can write its parent) is mounted as the link's target. Do not list a path inside a directory another box can write.

## GitHub access for agents

A boxed profile can carry a `github` binding. The credential broker then mints short-lived, read-only GitHub App installation tokens for the repositories you list, so the agent can `git clone`, `git fetch` and use `gh` without a personal access token in the box. A bound profile is refused on the host tier. On macOS its box goes through the same egress allowlist as on Linux; that is new and not yet verified on a Mac, and an existing VM needs `agent-box box setup -recreate` once (see [docs/MACOS.md](docs/MACOS.md#boxes-with-credential-bindings)).

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

Some services have no short-lived credential to mint: Grafana service-account tokens, Sentry auth tokens, Atlassian API tokens, a GitHub personal access token. A boxed profile can bind **static token entries** that you define once in `config.json`. The agent gets a token only for one command at a time, through a small in-box wrapper, and the box's egress is opened to exactly the hosts the entry names. A bound spawn is refused on the host tier. On macOS it goes through the egress allowlist like on Linux (new, not yet verified on a Mac; see [docs/MACOS.md](docs/MACOS.md#boxes-with-credential-bindings)).

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

## Several AWS accounts in one session

A boxed profile with the inline `aws` binding reaches one AWS account. To give one session several (a staging account and a production account, say), define **named AWS connections** once in `config.json` and let profiles pick them by name. Each connection is a full binding of its own: its own source profile, role, region, session policy, egress hosts and optional write role. Like the other credential bindings, a bound spawn is refused on the host tier, and on macOS it goes through the egress allowlist (new, not yet verified on a Mac; see [docs/MACOS.md](docs/MACOS.md#boxes-with-credential-bindings)).

**1. Define the connections.** Declare them next to `static_tokens` and restart agent-box:

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

Each entry takes the fields of the inline `aws` binding (see [docs/aws/agent-role](docs/aws/agent-role/README.md) for the roles) plus:

| Field | Rules |
|---|---|
| `name` | Required, unique. `^[a-z0-9][a-z0-9-]{0,62}$`. `default` and `agentbox` are reserved. Profiles refer to the connection by it, and it is the AWS profile name inside the box. |
| `description` | Optional, at most 200 characters, one line. Shown in the UI and in `agentbox-env --list`. |
| `region` | Required for every connection. |

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

Read-only comes from the role, not from agent-box: point each connection's `role_arn` at a read-only role and put write access only in `elevate.role_arn`. The session policy agent-box adds narrows data reads; it does not remove writes, so a connection whose `role_arn` can write can write at any time.

**Base-role check.** At spawn, agent-box asks each base role whether it can write. It runs IAM policy simulation of 11 broad write actions (`s3:PutObject`, `s3:DeleteObject`, `s3:CreateBucket`, `ec2:RunInstances`, `ec2:TerminateInstances`, `iam:CreateUser`, `iam:AttachRolePolicy`, `lambda:UpdateFunctionCode`, `rds:DeleteDBInstance`, `dynamodb:PutItem`, `cloudformation:CreateStack`) with the role's own credentials. The UI, `agentbox-env --list` and `agent-box doctor` then show one of:

- **role can write**, with the allowed actions.
- **no broad writes found**.
- **unverified**, with a short reason: `SSO expired`, `assume-role denied`, `role cannot simulate itself`, `timed out`, `check failed`, `not checked` or `probe disabled`.

"No broad writes found" is not proof of read-only. A write grant scoped to one resource or behind a condition is not detected, and permission boundaries and SCPs count only as far as IAM simulation evaluates them.

The result is fixed at spawn and cached for up to 1 hour. Respawn to pick up a role change. An unverified result is checked again after the next successful base (non-elevated) credential mint for that connection, for example after an SSO sign-in. The exceptions are `role cannot simulate itself` and `assume-role denied`: a sign-in cannot fix those, so the check stops. Fix the role, then respawn. A custom role needs `iam:SimulatePrincipalPolicy` on itself (`ReadOnlyAccess` includes it). The check only warns. It never refuses a spawn.

**Limits and operations**

- `aws_connections` is read at daemon start only: edit `config.json` and restart agent-box. A running session keeps the definition it was spawned with until you kill it, including a connection you have since removed from `config.json`; saving or spawning a profile that names a removed connection is refused.
- Each connection signs in through its own `source_profile`. If connections use different SSO sessions and one expires, only that connection's credential requests fail, and the UI shows which source to sign in to.
- `agent-box doctor` prints one `aws-connection <name>` line per connection: whether the `source_profile` exists in the host's AWS config (a missing one fails), whether its SSO login is valid or expired (checked once per SSO session, so connections sharing a login cost one check), and whether the connection can be elevated. It reads the shell's own AWS config, not the daemon's environment. A second line, `aws-connection <name> base role`, runs the base-role check above; inline `aws` bindings (from `config.json` and the profile store) get `aws-binding <profile>`. A base role that can write, or one that could not be checked, is a WARN; none found is a PASS. The check is never a FAIL.
- **Rolling back to an older binary (operator step).** An older agent-box ignores the `aws_connections` key, so a profile that uses it spawns **unbound**: no AWS credentials, and also no egress allowlist, so its box gets an open network like any unbound profile. Saving such a profile in the old UI drops the key permanently, and restored sessions get a fresh box. So before rolling back, kill every session that uses `aws_connections`, and do not edit those profiles in the old UI until you roll forward. Profiles with the inline `aws` binding are unaffected.

## Store

agent-box can hold secret values itself: tokens, a GitHub App private key, the Claude OAuth token. Anywhere config takes a value reference (`file` or `env`) it can also say `{"type": "store", "id": "<id>"}`, and `secret_sources` accepts `{"type": "store"}`. On disk there is only ciphertext (age), so the store directory is safe to back up.

**What it protects against, and what it does not**

- **It protects against a disk snapshot or backup** taken by your hosting provider or a backup job: the values in the store are unreadable without the passphrase (Linux) or the login Keychain item (macOS).
- **It does not hide values from agents.** While the store is unlocked, any process running as your user, a host-tier agent included, can read the master key and so every value. Boxed agents cannot. A value still reaches the box the way it always did (env for a declared secret, `agentbox-env` for a static token, the git helper for GitHub).
- **It only protects what you move into it.** A value that is also in an env file, a shell profile or your home directory is still readable from a snapshot. The same goes for everything else on the machine that agent-box does not own, such as SSH keys and cloud-CLI caches.
- It is not a vault product: one machine, one user, no sync.

**Commands.** The CLI honours `-config` like the rest of agent-box, writes the files directly and does not need the daemon running.

| Command | What it does |
|---|---|
| `agent-box store init` | Create the store. Linux asks for a passphrase (at least 12 characters; twice on a terminal, one line when piped) and leaves the store unlocked. macOS keeps the master key in the login Keychain. |
| `agent-box store status` | Initialized or not, locked or unlocked, time left, number of values. |
| `agent-box store unlock` | Linux: ask for the passphrase and unlock for `store.unlock_ttl`. |
| `agent-box store lock` | Linux: lock now. |
| `agent-box store passwd` | Linux: change the passphrase. Values are not re-encrypted. |
| `agent-box store set <id>` | Read a value from stdin and store it. The value is never an argument. On a terminal it is read without echo; when piped, all of stdin is read and one trailing newline is trimmed. Works while locked. |
| `agent-box store rm <id>` | Delete a value. |
| `agent-box store list` | Ids, last-updated time and what refers to each id. There is no `get`, and values are never printed. |

An id is 1 to 64 characters of letters, digits, `_` and `-`, starting with a letter or digit, and a value is at most 64 KiB.

**Unlock and TTL.** On Linux the master key sits in the kernel user keyring, not in the daemon, so unlocking survives a daemon restart. It does not survive a reboot. Unlock with `agent-box store unlock`, or from the web UI: a banner appears when the store is locked and Settings has a Lock button. Web unlock needs the browser session (an API bearer token cannot unlock) and failed attempts are rate limited. The key expires after `store.unlock_ttl` (default 7 days, a new unlock resets it, reads do not extend it); a lock or an expiry takes effect on the next value lookup. If you reach agent-box through a tunnel or proxy that terminates TLS, the passphrase is visible to that proxy in transit when you type it into the web UI. `agent-box store unlock` on the machine does not have that exposure. A forgotten passphrase cannot be recovered: remove `<data_dir>/store` and set the values again.

**After a reboot the store is locked.** Anything that needs a store value is then refused until someone unlocks: a spawn fails with `store is locked`, and a running boxed session's next token fetch gets a 503. If the Claude token is in the store, that means every Claude session. A host-tier agent hits this too.

**Moving the Claude token into the store.** Out of the box the token comes from the daemon's environment.

1. `printenv CLAUDE_CODE_OAUTH_TOKEN | agent-box store set CLAUDE_CODE_OAUTH_TOKEN`
2. Add `{"type": "store"}` to `secret_sources` **before** the `env` source, and restart agent-box.
3. Delete the value from every env file and shell profile that still holds it (the file your service manager loads as `EnvironmentFile=`, your dev and prod copies, your login shell's profile).
4. Restart agent-box and check for leftovers. At start the daemon logs one line for each name the store serves that is still in the daemon's own environment, and Settings shows the same list for the running daemon. `agent-box doctor` only checks the environment it is run from, so it does not see what your service manager gives the daemon.

The protection exists only once every plaintext copy is gone.

**Docker's seccomp profile must stay on.** The box barrier for the master key is Docker's default seccomp profile, which refuses the `keyctl` calls a box would need to read it. On Linux, at each boxed spawn agent-box checks that seccomp is active, and when it is not (and a store exists) it refuses the spawn, and `agent-box doctor` fails. A box that was already running when you ran `agent-box store init` is not re-checked: a container keeps the seccomp profile it was created with, so a box created while seccomp was off can read the keyring once the store is unlocked. If your host runs Docker with seccomp off, stop those boxes before `store init`. macOS boxes run in a VM and the store key stays in the host Keychain, so this gate does not apply there.

**On macOS** there is no lock cycle: the Keychain holds the master key, `status` shows `expires: never`, and `unlock`, `lock` and `passwd` do nothing.

## Security model

agent-box starts shells and agents on your machine, so authentication is never optional.

**What is protected, and how**

- **Login.** Either a generated token (printed once in a URL, kept at `0600` in the data root) or a password from the env var named by `password_env`. Failed logins are throttled. Browsers get a signed 30-day cookie; delete `cookie.secret` to log everyone out. Named, revocable bearer tokens exist for programmatic clients and are stored as hashes.
- **Network exposure.** The default bind is loopback. Binding all interfaces is refused unless `allow_public` is set. Requests whose `Host` header is not in the allowlist are refused (DNS-rebinding defence), and state-changing requests are checked for same-origin.
- **Containment.** The boxed tier described above. Roughly, containment comes from what is absent inside the box. The hardening flags are the smaller part. A box cannot reach services on the host's loopback, other than agent-box's own credential broker and, for boxes with a credential binding, the egress proxy; on macOS agent-box checks this on every new box and refuses the box if the check fails.
- **Cloud access.** The broker mints credentials only for live boxed sessions, with a per-spawn token, rate limits, and a 15-minute lifetime. The role is capped by `ReadOnlyAccess`, and a generated session policy denies reading S3 objects (outside an allowlist you set per binding), Secrets Manager values, SSM parameters, DynamoDB items and KMS decrypt. Trust is limited to the SSO permission sets you name and requires a `SourceIdentity` of `agentbox-*`. Create the role with [docs/aws/agent-role](docs/aws/agent-role/README.md). The inline `aws` binding gets the same base-role write check as named connections (see [Several AWS accounts in one session](#several-aws-accounts-in-one-session)).
- **Store.** Values held in the [store](#store) are encrypted against disk snapshots and backups. While it is unlocked, host-tier agents can read them; boxed agents cannot.
- **Static tokens.** A bound token is fetched per command through the broker, is audited, and reaches only the hosts its entry names. The agent can still read it, and it is long-lived and not revoked when the session ends. See [Static tokens for agents](#static-tokens-for-agents).

**Never bind agent-box to a public interface without a front door.** A reachable agent-box is a remote shell. If you need access from elsewhere, keep the bind on loopback or a private network and put an authenticating proxy or tunnel in front (add its hostname to `allowed_hosts`). Do not rely on the login page alone on the open internet, and use a long password.

**Known limits**

- The `host` tier is not contained. A host session is you, with an agent.
- The adversary in scope is an agent running arbitrary userspace commands in a box. A kernel-level escape is out of scope.
- On Linux, boxes reach the network only through agent-box's CONNECT proxy: port 443, hostnames only, never private addresses or addresses on the host's own interfaces. A public IP that is NAT'd to the host but not on one of its interfaces is not known to agent-box. AWS-bound boxes are limited to an allowlist. Boxes on the egress network cannot reach each other. On macOS only boxes with a binding (an `aws` binding, named AWS connections, a `github` binding or static tokens) get this treatment, and it is new and not yet verified on a Mac; macOS boxes without a binding keep normal outbound access.
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

- Verify the egress allowlist for macOS boxes on a Mac (built, not yet run there).

## Development

```sh
scripts/test.sh
```

This runs `gofmt`, `go test ./...`, a macOS cross-build and `go vet`, the browser-side tests (`node --test`) and a lint of the page's script references.

## License

Apache-2.0. See [LICENSE](LICENSE).
