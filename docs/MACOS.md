# Running agent-box on macOS

Sessions run as your user (host tier, **not contained**) unless you set up the
boxed tier, which runs them in a container inside agent-box's own colima VM; see
[Boxed tier](#boxed-tier-contained-sessions) below.

## Install (from source)

    brew install go abduco
    tar xzf agent-box-src-<sha>.tar.gz && cd agent-box-src-<sha>
    go build -ldflags "-X main.commit=<sha>" -o agent-box ./cmd/agent-box
    go test ./internal/procinfo/... ./internal/abduco/... ./internal/session/... ./internal/config/...

A binary you build yourself is not quarantined by Gatekeeper.

## Check the machine

    ./agent-box doctor

Every line is PASS, WARN or FAIL. The report contains no secrets, so it is safe
to paste into an issue.

## Run

    ./agent-box serve

With no config file, agent-box listens on `127.0.0.1:8899` and prints a login
URL containing a login token (`#token=…`), generated on first start and kept in
`~/.local/share/agent-box/login.token`. Open the URL in a browser on the same Mac.

Closing the terminal tab stops agent-box but not your sessions; run
`agent-box serve` again and they reattach. Only one agent-box can serve a data
directory at a time.

## Configure

Everything agent-box writes lives in one directory, `~/.local/share/agent-box`
(set `AGENT_BOX_HOME` to an absolute path to move it): its config, login token,
session records, profiles and analytics. Back it up, move it or delete it as a
unit. Claude Code's own files stay in `~/.claude`; agent-box only reads them. The one
exception is the abduco socket directory, `/tmp/agent-box-<uid>-<hash>`, because macOS
caps a socket path at 103 bytes; it holds only live sockets.

Optional config: `~/.local/share/agent-box/config.json` (see `config.example.json`), or
pass `-config <path>`. When `AGENT_BOX_HOME` is unset, `~/.config/agent-box/config.json`
(or `$XDG_CONFIG_HOME/agent-box/config.json`) is still read as a fallback. A
`config.json` in the current directory is deliberately not read, so running agent-box
inside a cloned repo never loads that repo's config. To reach
agent-box through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

Both files sit in the data directory (default `~/.local/share/agent-box`).

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.

## Boxed tier (contained sessions)

A boxed session runs in a container inside agent-box's **own** colima VM, which
lives under the data root (`<data root>/vm`). It never becomes your docker
context and never edits `~/.ssh/config`.

    brew install colima docker
    agent-box box setup        # once: creates the VM, builds the image natively (or pulls it when `box_image` is a ghcr digest), checks containment
    agent-box doctor           # the "boxed tier", "box image" and "containment" lines

- The VM starts on the first boxed spawn (expect 20–30 s) and keeps running.
  `agent-box box stop` frees its memory (default 4 CPU / 4 GiB; `box_vm_cpus`,
  `box_vm_memory_gib` in config.json). It refuses while any box is running, even
  an idle one; `agent-box box stop -force` stops the VM anyway and ends those
  sessions. `agent-box box status` shows the VM, the image and the boxes.
- Workspaces must be under your home directory (the VM shares only that), and
  must not be, contain or sit inside the data root (it holds the VM's own
  settings and SSH key). They also must not be or contain the directory of the
  config file agent-box reads, or of `profiles_path` (symlinks are followed to
  their targets); a folder below those is fine.
- A box cannot reach services listening on your Mac's 127.0.0.1, other than
  agent-box's own credential broker and, for boxes with a binding, the egress
  proxy (see below). agent-box checks this on every new box and refuses the box
  if the check fails.
- Claude Code keeps its macOS login in the Keychain, which a box cannot read.
  Create a token for boxes with `claude setup-token`, store it as a secret
  source, and add `CLAUDE_CODE_OAUTH_TOKEN` to the agent's `secrets`.
- Requirements: macOS 13+, Apple Silicon (Intel Macs untested), colima 0.8+.
- Not yet tested: company VPN/split-DNS from inside a box.
- File-heavy work (`npm install`, large builds) is slower boxed than on the host
  tier: the workspace crosses a VM file share (virtiofs).
- If doctor warns about the VM's mount type, `agent-box box setup -recreate`
  deletes and recreates the VM. Like `box stop`, it refuses while any box is
  running, and also when the VM is not running (its boxes cannot be checked
  then); add `-force` to go ahead and end any sessions in it.

### Boxes with credential bindings

A profile with any binding (an `aws` binding, named AWS connections, a `github`
binding or static tokens) gets a locked-down network. Its box can reach only the
credential broker and agent-box's egress proxy on your Mac (the proxy listens
on `127.0.0.1`, port `egress_port`, default 9072; boxes call it
`host.lima.internal`), and the proxy lets through only the hosts the
profile's bindings allow. Everything else is dropped by rules inside the VM:
direct connections, DNS, and other boxes. Profiles without a binding are
unchanged and keep normal outbound access.

Egress control for macOS boxes is new and has not yet been verified on a Mac.

- After upgrading, run `agent-box box setup -recreate` once. The rules live in
  the VM's provisioning, so an existing VM has none until it is recreated;
  `agent-box doctor` warns "box VM config changed since setup" until you do.
  Like any recreate, it refuses while a box is running unless you add `-force`.
- The bound network uses the fixed subnet `172.29.72.0/24` inside the VM.
- A tool that ignores `HTTPS_PROXY` cannot connect, by design: there is no
  direct route to fall back on. A connection that fails with a timeout, not a
  clear error, is usually such a tool.
- Denials show in the steer rail's egress row for the session ("N denied since
  start") and, with host and time, in the `egress_audit` table of `audit.db` in
  the data directory.
