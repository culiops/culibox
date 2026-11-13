# CuliBox

A self-hosted web control plane for running, watching, containing and steering AI coding-agent sessions.

CuliBox is the safe way to let an AI agent touch your cloud. You start Claude Code sessions from a browser, each one in its own container by default, and you keep an eye on what they are doing and what they cost. AWS access, when you grant it, is short-lived and scoped, and every grant is logged.

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
- **Clone mode.** An agent works on a private copy of a repo, and its branches come home as `box/<branch>` for you to review. Linux, and macOS with the boxed tier. See [Clone mode](docs/CLONE-MODE.md).
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
- **`culibox doctor`.** A read-only machine check that prints PASS, WARN or FAIL lines, safe to paste into an issue.

## Requirements

- Go 1.26.5 or newer (see `go.mod`) to build.
- Linux or macOS. macOS needs 13+ on Apple Silicon (Intel is untested) for the boxed tier.
- [abduco](https://www.brain-dump.org/projects/abduco/) on `PATH`. It is required; the daemon will not start without it.
- For the boxed tier: Docker on Linux, or `colima` and `docker` on macOS. On Linux the docker daemon must run on the same machine and see host files as they are (a local Docker Engine). A VM-backed or remote daemon (for example Docker Desktop for Linux or a remote docker context) is not supported for boxed sessions: CuliBox refuses to start a box there, because it cannot confirm the box got the folder that was checked.
- The agent CLI you want to run. Claude Code (`claude`) is the supported agent. Other terminal programs can be configured as agents (a shell, for example), but status, usage and reply features are Claude Code only.
- Node.js, only to run the browser-side tests in development.

## Install

Each release on the [Releases page](https://github.com/culiops/culibox/releases) has a tarball for Linux and for macOS, each for amd64 and arm64, and a `SHA256SUMS` file. A tarball holds `culibox`, `LICENSE` and `README.md`. It does not include abduco or Docker; see [Requirements](#requirements). To build from source instead, use [From source (manual)](#from-source-manual). The [Quick start](#quick-start) installer does the download and the check below for you.

On Linux (use `arm64` on an ARM machine, and the release you want in place of `0.1.0`):

```sh
VERSION=0.1.0
OS=linux
ARCH=amd64
base=https://github.com/culiops/culibox/releases/download/v$VERSION
curl -fsSLO "$base/culibox_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
grep "culibox_${VERSION}_${OS}_${ARCH}.tar.gz" SHA256SUMS | sha256sum -c -
mkdir culibox && tar -xzf "culibox_${VERSION}_${OS}_${ARCH}.tar.gz" -C culibox
cd culibox
./culibox version
```

On macOS, set `OS=darwin` (`ARCH=arm64` on Apple Silicon) and check with `shasum` instead:

```sh
grep "culibox_${VERSION}_${OS}_${ARCH}.tar.gz" SHA256SUMS | shasum -a 256 -c -
```

The check must print `OK` for your tarball. `version` prints `culibox v<version> (<commit>)`.

**macOS binaries are unsigned.** A file downloaded with `curl` is not quarantined and runs as it is. A file downloaded in a browser is quarantined, so remove the flag before the first run:

```sh
xattr -d com.apple.quarantine culibox
```

**Is the macOS build verified?** Every release's notes end with a Platform status block. The Linux lines say the build was tested in CI, with a link to the run. The macOS lines say one of these:

- `⚠️ NOT yet verified on macOS`: nobody has checked these files on a Mac yet.
- `✅ verified on macOS ...`: the exact published files were checked on a Mac. The line gives the macOS version, the chip and the date.
- `⚠️ automated checks passed, manual checks not run`, or `⚠️ automated checks incomplete`: only part of the check ran.
- `❌ failed: ...`: a check failed. Do not use that build.

Releases are checked on a Mac in batches, so a new release usually starts as not verified. The block's last line, "Last macOS-verified release", names the newest verified release at the time this release was cut. It is not updated afterwards, so check the Releases page to see whether a newer release has been verified since. If you want a verified macOS build, pick the newest release marked ✅. The amd64 build is checked on an Apple Silicon Mac under Rosetta 2, not on Intel hardware. How the check works is in [docs/MACOS.md](docs/MACOS.md#release-verification).

## Quick start

**1. Install and set up.** Download the installer, read it, run it (use the release tag you want in place of `<tag>`):

```sh
curl -fsSLO https://raw.githubusercontent.com/culiops/culibox/<tag>/scripts/setup.sh
sh setup.sh
```

`setup.sh` is a short POSIX `sh` script. It installs a checksum-verified `culibox` into `~/.local/bin` (it never uses `sudo` and installs only under that folder; it downloads and builds in a temporary folder that it removes, and with `--from-source` on macOS it may run `brew install go` after an explicit `y`), then runs `culibox init`. `init` is a guided setup that checks the programs CuliBox needs (offering `brew install` on macOS, one package at a time, and only printing commands on Linux), your workspace folder, the macOS box VM size, a Claude token, the config file, `box setup` and `culibox doctor`. Each of its 8 steps prints one result line, and you can run it again at any time to continue. Every step of both, so you can do it by hand, is in [docs/MACOS.md](docs/MACOS.md#install). Options: `--version vX.Y.Z`, `--from-source` (build that release instead of downloading a binary) and `--prefix DIR`.

Until a release that includes `culibox init` is published (the script requires `v0.34.0` or newer), plain `sh setup.sh` refuses by design. Until then only `sh setup.sh --from-source --version <a pushed tag, v0.34.0 or newer>` works, or follow [From source (manual)](#from-source-manual). While the repository is private, `raw.githubusercontent.com` answers 404: run `sh scripts/setup.sh` from a clone, or fetch the script with `gh api` (see [docs/MACOS.md](docs/MACOS.md#install)).

**2. Start CuliBox.**

```sh
culibox serve
```

CuliBox listens on `127.0.0.1:8899` and prints a login URL with a login token. Open it in a browser on the same machine. The token is generated on the first start and kept in `login.token` in the data root; it stays valid across restarts until you delete that file (which makes a new one).

### From source (manual)

This is what `setup.sh --from-source` and `culibox init` do for you.

**1. Build.** Build into `~/.local/bin`, not into the source folder: a folder that holds a program CuliBox runs can only be shared with the box VM read-only.

```sh
git clone https://github.com/culiops/culibox.git
cd culibox
mkdir -p ~/.local/bin
go build -ldflags "-X main.version=$(scripts/version.sh)" -o ~/.local/bin/culibox ./cmd/culibox
culibox doctor
```

Add `~/.local/bin` to your `PATH` if it is not there. Releases are `vX.Y.Z` git tags. `scripts/version.sh` prints the checkout's version (`v0.1.0`, or `v0.1.0-3-g<sha>` for 3 commits past the tag). The header shows it, and the About tab and `culibox bug-report` name it. A build without the flag works but shows no version.

**2. Try it uncontained.** With no config file, CuliBox runs one `claude` agent on the **host** tier (not contained):

```sh
culibox serve
```

It prints a login URL with a login token. Open it in a browser on the same machine. The token is kept in `login.token` in the data root and stays valid until you delete the file.

**3. Turn on boxes.** Build the box image (on macOS this also creates the VM; see [docs/MACOS.md](docs/MACOS.md)):

```sh
culibox box setup
```

`box setup` tags every build with its date (for example `culiops/culibox:2026-10-18`) and prints how to pin it. To pull instead of build, set `"box_image": "ghcr.io/culiops/culibox@sha256:<digest>"` (the digest is in the CI job summary, or from `docker buildx imagetools inspect ghcr.io/culiops/culibox:base`) and run `box setup`: it pulls the image for your daemon's architecture instead of building it. Pin by digest, never by `:base`.

Then put this in `~/.local/share/culibox/config.json` and run `culibox serve` again:

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

Open `http://127.0.0.1:8899` (use the printed login URL the first time). Sessions are boxed unless a profile says `"runtime": "host"`. The workspace folder must not be your home directory, because a box mounts the whole workspace. Claude Code needs a login inside the box: create a long-lived token with `claude setup-token` and paste it on an agent account's row in Settings → Agent accounts (see [Features](docs/FEATURES.md); `culibox init` does this for an account named `claude-box`), or store it as a secret (see `secret_sources` in [Configuration](docs/CONFIG.md)) and list `CLAUDE_CODE_OAUTH_TOKEN` under the agent's `secrets`.

Every key is in [Configuration](docs/CONFIG.md); [config.example.json](config.example.json) is a fuller example. Only one CuliBox can serve a data root at a time.

## Upgrading from agent-box

CuliBox used to be called agent-box. An existing install keeps working with the new `culibox` binary:

- **Changes.**
  - The binary and every command are now `culibox`. Replace `agent-box` in your service file or scripts.
  - The environment variable is `CULIBOX_HOME`. `AGENT_BOX_HOME` still works and prints a notice. If both are set to different paths, CuliBox refuses to start.
  - New installs build the box image as `culiops/culibox:base` and pull from `ghcr.io/culiops/culibox`.
- **Moves on its own.** On the first start of `culibox serve`, the data folder `~/.local/share/agent-box` is moved to `~/.local/share/culibox`, and a link is left at the old path. Your store key, profiles, accounts and history come along. It waits for a start when no session is running, and it does not move a folder you set with `CULIBOX_HOME`/`AGENT_BOX_HOME`. On a Mac with a box VM it stays where it is. `culibox doctor` shows which folder is in use, and why. A `~/.config/agent-box/config.json` moves to `~/.config/culibox/` the same way.
- **Stays.**
  - A `box_image` you pinned keeps working. If you never set one, your existing image is reused.
  - Keychain and keyring entry names, the colima profile `agent-box`, container labels and browser layout settings keep their old names. Nothing needs to be redone.

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
| [Remote access](docs/REMOTE-ACCESS.md) | Reaching CuliBox from another machine, and why notifications need HTTPS or localhost. |
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
