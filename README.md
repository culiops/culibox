# CuliBox

A self-hosted web control plane for running, watching, containing and steering AI coding-agent sessions.

CuliBox is the safe way to let an AI agent touch your cloud. You start Claude Code sessions from a browser, each one in its own container by default, and you keep an eye on what they are doing and what they cost. AWS access, when you grant it, is short-lived and scoped, and every grant is logged.

The binary, CLI and box image are still named `agent-box`.

## Why it exists

Letting an agent run commands against real infrastructure is useful and risky. The usual setup is a terminal on a machine that holds your SSH keys, your cloud credentials and your whole home directory, with the agent running as you.

CuliBox puts a boundary around that. Sessions run in a per-project container where only the project folder is mounted writable. Cloud credentials are minted per session, last 15 minutes and are narrower than your own. You see every session in one place, including which ones are waiting for you.

It is built first for ops, cloud, DevOps and SRE work, where you want a control panel rather than an editor.

**What it is not:** an IDE or a code editor. There is a small file browser and editor for quick fixes, and it is kept deliberately thin. If you want to edit code all day, use an editor.

## Features

One line each here; every detail is in [docs/FEATURES.md](docs/FEATURES.md).

**Watch**

- **Multi-session web terminal.** Several agent sessions side by side in the browser, on desktop or phone.
- **Attention awareness.** Each session shows whether it is working, idle or waiting for you, and why. The tab title and favicon change while something needs you.
- **Usage and cost.** Per-session context size and model, account-level 5-hour and 7-day rate-limit windows (when a statusLine hook provides them), and a local analytics view of tokens and estimated cost per model, project and session.
- **Environment badge.** Mark an instance as DEV, PROD and so on.

**Run and steer**

- **Session persistence.** Sessions run under [abduco](https://www.brain-dump.org/projects/abduco/) and survive a daemon restart or a closed browser.
- **Steer from the UI.** Interrupt a session, restart an exited one (host-tier Claude Code sessions resume the same conversation), and, with `replies_enabled` on, answer permission prompts from the browser.
- **Profiles.** Define agents in config or in the UI, with launch flags and env vars as toggles.
- **Files.** A thin file browser: view, quick edit, upload, download.

**Contain**

- **Boxed runtime (the default).** Each project gets its own Docker container where only the project folder is mounted writable: no host home, no `~/.ssh`, no Docker socket, all capabilities dropped, a read-only root, and PID and memory caps. With a config file, boxes are the default and the `host` tier (no containment) is something you choose; with no config file at all, CuliBox runs on the host tier.
- **Your agent config, filtered.** A box gets an allowlisted slice of your Claude Code config (`CLAUDE.md` and the files it imports, skills, plugins, MCP servers that run in the image). Credentials, history and transcripts are never copied. See [CLAUDE.md imports](docs/CLAUDE-MD-IMPORTS.md).
- **Clone mode.** An agent works on a private copy of a repo, and its branches come home as `box/<branch>` for you to review. Linux only. See [Clone mode](docs/CLONE-MODE.md).
- **Read other repos.** `context_paths` mounts partner repos or docs into a box read-only. See [Reading other repos](docs/CONTEXT-PATHS.md).
- **A ready box shell.** zsh with completion for aws, terraform, kubectl, helm and gh, and a prompt that shows production contexts in red.
- **macOS.** Boxes run in CuliBox's own colima VM. See [docs/MACOS.md](docs/MACOS.md).
- **Multiple accounts.** Named agent accounts, each with its own config and token, kept apart from your personal login.

**Cloud access, audit and secrets**

- **AWS credential broker.** Per-session credentials that last 15 minutes, minted from your SSO session through a role you create to be read-only, and narrowed further. The box never sees your SSO session. One session can reach several accounts, and a write role can be granted for a fixed window. The opt-in SSO-direct mode is the exception: its credentials keep your SSO role's expiry (hours, not minutes), are not revoked when the session ends, and CloudTrail shows you. See [AWS access](docs/AWS.md).
- **GitHub access.** One-hour GitHub App tokens scoped to the repos you list, read-only by default. See [GitHub access](docs/GITHUB.md).
- **Static tokens.** Grafana, Sentry, Atlassian or a GitHub personal access token, handed to one command at a time, with egress opened only to that service's hosts. The agent can read a bound token, and it is long-lived and not revoked when the session ends. See [Static tokens](docs/STATIC-TOKENS.md).
- **Audit.** CloudTrail records the agent session rather than you (not in SSO-direct mode), and every mint, refusal and token fetch is written to a local audit log, never the value.
- **Secrets by reference, and an encrypted store.** Config names secrets instead of holding them. An optional encrypted store keeps values unreadable in disk snapshots and backups. See [Store](docs/STORE.md).
- **`agent-box doctor`.** A read-only machine check that prints PASS, WARN or FAIL lines, safe to paste into an issue.

## Requirements

- Go 1.26.5 or newer (see `go.mod`) to build.
- Linux or macOS. macOS needs 13+ on Apple Silicon (Intel is untested) for the boxed tier.
- [abduco](https://www.brain-dump.org/projects/abduco/) on `PATH`. It is required; the daemon will not start without it.
- For the boxed tier: Docker on Linux, or `colima` and `docker` on macOS.
- The agent CLI you want to run. Claude Code (`claude`) is the supported agent. Other terminal programs can be configured as agents (a shell, for example), but status, usage and reply features are Claude Code only.
- Node.js, only to run the browser-side tests in development.

## Quick start

**1. Build.**

```sh
git clone https://github.com/culiops/culibox.git
cd culibox
go build -ldflags "-X main.version=$(scripts/version.sh)" -o agent-box ./cmd/agent-box
./agent-box doctor
```

Releases are `vX.Y.Z` git tags. `scripts/version.sh` prints the checkout's version (`v0.1.0`, or `v0.1.0-3-g<sha>` for 3 commits past the tag). The header shows it, and the About tab and `agent-box bug-report` name it. A build without the flag works but shows no version.

**2. Try it uncontained.** With no config file, CuliBox listens on `127.0.0.1:8899` and runs one `claude` agent on the **host** tier (not contained):

```sh
./agent-box serve
```

It prints a login URL with a one-time token. Open it in a browser on the same machine. The token is kept in `login.token` in the data root.

**3. Turn on boxes.** Build the box image (on macOS this also creates the VM; see [docs/MACOS.md](docs/MACOS.md)):

```sh
./agent-box box setup
```

`box setup` tags every build with its date (for example `culiops/agent-box:2026-10-18`) and prints how to pin it. To pull instead of build, set `"box_image": "ghcr.io/culiops/agent-box@sha256:<digest>"` (the digest is in the CI job summary, or from `docker buildx imagetools inspect ghcr.io/culiops/agent-box:base`) and run `box setup`: it pulls the image for your daemon's architecture instead of building it. Pin by digest, never by `:base`.

Then put this in `~/.local/share/agent-box/config.json` and run `./agent-box serve` again:

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

Open `http://127.0.0.1:8899` (use the printed login URL the first time). Sessions are boxed unless a profile says `"runtime": "host"`. The workspace folder must not be your home directory, because a box mounts the whole workspace. Claude Code needs a login inside the box: create a long-lived token with `claude setup-token`, store it as a secret (see `secret_sources` in [Configuration](docs/CONFIG.md)), and list `CLAUDE_CODE_OAUTH_TOKEN` under the agent's `secrets`.

Every key is in [Configuration](docs/CONFIG.md); [config.example.json](config.example.json) is a fuller example. Only one CuliBox can serve a data root at a time.

## Security

CuliBox starts shells and agents on your machine, so login is always required. The default bind is loopback, and binding all interfaces is refused unless `allow_public` is set.

**Never bind CuliBox to a public interface without a front door.** A reachable CuliBox is a remote shell. If you need access from elsewhere, keep the bind on loopback or a private network and put an authenticating proxy or tunnel in front (add its hostname to `allowed_hosts`). Do not rely on the login page alone on the open internet, and use a long password.

Know the limits before you rely on it:

- The `host` tier is not contained. A host session is you, with an agent.
- The adversary in scope is an agent running arbitrary userspace commands in a box. A kernel-level escape is out of scope.
- An agent can read any credential it is given while it runs. Short-lived credentials (the AWS agent role, GitHub App tokens) are limited by lifetime, scope, the egress lock and the audit log. Static tokens, GitHub personal access tokens and SSO-direct credentials are long-lived and are not revoked when the session ends.
- After a boxed session, review changes and untracked files (`git status`, `git diff`) before you run host git or a host-tier session in that workspace.
- Use the AWS broker with a sandbox or lab account for now, not production.

The full model and every known limit: [docs/SECURITY.md](docs/SECURITY.md).

## Documentation

| Doc | What it covers |
|---|---|
| [Features](docs/FEATURES.md) | Every feature in detail: boxes, config bundle, box shell, credentials. |
| [Configuration](docs/CONFIG.md) | The data root, every `config.json` key, profile fields. |
| [Clone mode](docs/CLONE-MODE.md) | Agents working on a private copy of a repo; bringing branches home. |
| [Reading other repos](docs/CONTEXT-PATHS.md) | `context_paths`: read-only mounts of other folders. |
| [CLAUDE.md imports](docs/CLAUDE-MD-IMPORTS.md) | How files your `CLAUDE.md` imports reach a box. |
| [GitHub access](docs/GITHUB.md) | GitHub App tokens for `git` and `gh` in a box. |
| [Static tokens](docs/STATIC-TOKENS.md) | Long-lived service tokens, self-hosted services, GitHub PATs. |
| [AWS access](docs/AWS.md) | Several AWS accounts per session, write windows, the opt-in SSO-direct mode. |
| [AWS agent role](docs/aws/agent-role/README.md) | The Terraform for the IAM role the broker assumes. |
| [Store](docs/STORE.md) | The encrypted value store and its commands. |
| [Security model](docs/SECURITY.md) | What is protected, how, and the known limits. |
| [macOS](docs/MACOS.md) | Install, the colima-based boxed tier, rotating credentials. |
| [Docker](docs/DOCKER.md) | Running CuliBox itself in a container, and keeping sessions across restarts. |

## Roadmap

- Verify on a Mac what is built but not yet run there: credentials minted through the broker (AWS, GitHub, static tokens) and the Keychain failure states.

## Development

```sh
scripts/test.sh
```

This runs `gofmt`, `go test ./...`, a macOS cross-build and `go vet`, the browser-side tests (`node --test`) and a lint of the page's script references.

## License

Apache-2.0. See [LICENSE](LICENSE).
