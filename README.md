# CuliBox

A self-hosted web control plane for running, watching, containing and steering AI coding-agent sessions.

CuliBox is the safe way to let an AI agent touch your cloud. You start Claude Code sessions from a browser, each one in its own container by default, and you keep an eye on what they are doing and what they cost. AWS access, when you grant it, is short-lived and scoped, and every grant is logged.

**Status:** pre-1.0 and in active development. CuliBox is used every day to build CuliBox itself. Until 1.0, a minor release can change config keys or behaviour, so read the release notes before you upgrade.

## Why it exists

Letting an agent run commands against real infrastructure is useful and risky. The usual setup is a terminal on a machine that holds your SSH keys, your cloud credentials and your whole home directory, with the agent running as you.

CuliBox puts a boundary around that. Sessions run in a per-project container where only the project folder is mounted writable. Cloud credentials are minted per session, last 15 minutes and are narrower than your own. You see every session in one place, including which ones are waiting for you.

It is built first for ops, cloud, DevOps and SRE work, where you want a control panel rather than an editor.

![Fig. 1: the boundary of one box. Kept outside, on your machine: SSH keys, home directory, Docker socket, AWS SSO session, shell history. Inside the box: the agent, the project folder read-write, a filtered copy of the agent config. Crossing the wall only through gates: AWS for 15 minutes, GitHub for 1 hour, static tokens for one command, and the internet through the proxy. Every grant goes to a local audit log.](docs/images/box-boundary.svg)

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
- **Profiles.** Define agents in the UI or in config, with launch flags and env vars as toggles.
- **Files.** A thin file browser: view, quick edit, upload, download.

**Contain**

- **Boxed runtime (the default).** Each project gets its own Docker container where only the project folder is mounted writable: no host home, no `~/.ssh`, no Docker socket, all capabilities dropped, a read-only root, and PID and memory caps. With a config file, boxes are the default and the `host` tier (no containment) is something you choose; with no config file at all, CuliBox runs on the host tier.
- **Your agent config, filtered.** A box gets an allowlisted slice of your Claude Code config (`CLAUDE.md` and the files it imports, skills, plugins, MCP servers that run in the image). Credentials, history and transcripts are never copied. See [CLAUDE.md imports](docs/CLAUDE-MD-IMPORTS.md).
- **Clone mode.** An agent works on a private copy of a repo, and its branches come home as `box/<branch>` for you to review. Linux, and macOS with the boxed tier. See [Clone mode](docs/CLONE-MODE.md).
- **Read other repos.** `context_paths` mounts partner repos or docs into a box read-only. See [Reading other repos](docs/CONTEXT-PATHS.md).
- **A ready box shell.** zsh with completion for aws, terraform, kubectl, helm and gh, and a prompt that shows production contexts in red.
- **macOS.** Boxes run in CuliBox's own colima VM. See [docs/MACOS.md](docs/MACOS.md).
- **Multiple accounts.** Named agent accounts, each with its own config and token, kept apart from your personal login.

**Cloud access, audit and secrets**

- **AWS credential broker.** Per-session credentials that last 15 minutes, minted from your SSO session through a role you create to be read-only, and narrowed further. The box never sees your SSO session. One session can reach several accounts, and a write role can be granted for a fixed window. The opt-in SSO-direct mode is the exception: its credentials keep your SSO role's expiry (hours, not minutes), are not revoked when the session ends, and CloudTrail shows you. See [AWS access](docs/AWS.md), which also has a diagram of how a credential reaches the agent.
- **GitHub access.** One-hour GitHub App tokens scoped to the repos you list, read-only by default. See [GitHub access](docs/GITHUB.md).
- **Static tokens.** Grafana, Sentry, Atlassian or a GitHub personal access token, handed to one command at a time, with egress opened only to that service's hosts. The agent can read a bound token, and it is long-lived and not revoked when the session ends. See [Static tokens](docs/STATIC-TOKENS.md).
- **Audit.** CloudTrail records the agent session rather than you (not in SSO-direct mode), and every mint, refusal and token fetch is written to a local audit log, never the value.
- **Secrets by reference, and an encrypted store.** A credential entry names where its value lives instead of holding it. An optional encrypted store keeps values unreadable in disk snapshots and backups. See [Store](docs/STORE.md).
- **`culibox doctor`.** A read-only machine check that prints PASS, WARN or FAIL lines, safe to paste into an issue.

## Quick start

You need Linux or macOS and Claude Code. Boxed sessions also need Docker on Linux, or `colima` and `docker` on macOS 13+ on Apple Silicon (Intel is untested). `culibox init` checks these programs and the macOS version, and says how to install what is missing. Sessions run under [abduco](https://www.brain-dump.org/projects/abduco/), which each release ships next to `culibox`. The full list is in [Requirements](docs/INSTALL.md#requirements).

Download the installer from the newest release, read it, and run it:

```sh
curl -fsSLO https://github.com/culiops/culibox/releases/latest/download/setup.sh
sh setup.sh
```

It installs the newest release's checksum-verified `culibox`, with the `abduco` that comes with it, into `~/.local/bin` without `sudo` (it skips both when that release is already installed), then runs `culibox init`, a guided setup you can run again at any time. `sh setup.sh --version vX.Y.Z` installs another release. Then start CuliBox:

```sh
culibox serve
```

It listens on `127.0.0.1:8899` and prints a login URL. Open it in a browser on the same machine.

Release tarballs, building from source, what `setup.sh` and `init` do step by step, and how to tell whether a macOS build is verified: [docs/INSTALL.md](docs/INSTALL.md). Upgrading: [docs/UPGRADING.md](docs/UPGRADING.md).

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

**Report a vulnerability privately**, as described in [`.github/SECURITY.md`](.github/SECURITY.md) (GitHub private vulnerability reporting), never in a public issue.

## Documentation

| Doc | What it covers |
|---|---|
| [Install](docs/INSTALL.md) | Requirements, the installer, release tarballs, building from source, macOS release verification. |
| [Features](docs/FEATURES.md) | Every feature in detail: boxes, config bundle, box shell, credentials. |
| [Box image](docs/BOX-IMAGE.md) | Every tool, MCP server and Debian package in the box image, at which version, and how it is scanned. |
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
| [Remote access](docs/REMOTE-ACCESS.md) | Reaching CuliBox from another machine, and why notifications need HTTPS or localhost. |
| [macOS](docs/MACOS.md) | Install, the colima-based boxed tier, rotating credentials. |
| [Docker](docs/DOCKER.md) | Running CuliBox itself in a container, and keeping sessions across restarts. |
| [Upgrading](docs/UPGRADING.md) | What an upgrade keeps, the automatic backup, rolling back, and installs from the agent-box days. |
| [Contributing](CONTRIBUTING.md) | How to file a bug or propose a change. Issues only. |

## Roadmap

- Verify on a Mac what is built but not yet run there: credentials minted through the broker (AWS and GitHub) and the Keychain failure states.

## Development

CuliBox takes bug reports and proposals as issues; outside pull requests are closed with thanks. See [CONTRIBUTING.md](CONTRIBUTING.md).

To run the checks:

```sh
scripts/test.sh
```

This runs `gofmt`, `go test ./...`, a macOS cross-build and `go vet`, the browser-side tests (`node --test`) and a lint of the page's script references, and more (the upgrade test, the installer and deploy script tests, secret-scan rules and extra vet builds); see `scripts/test.sh`.

## License

Apache-2.0. See [LICENSE](LICENSE).
