# Running CuliBox on macOS

Sessions run as your user (host tier, **not contained**) unless you set up the
boxed tier, which runs them in a container inside CuliBox's own colima VM; see
[Boxed tier](#boxed-tier-contained-sessions) below.

## Install (from source)

    brew install go abduco
    tar xzf agent-box-src-<sha>.tar.gz && cd agent-box-src-<sha>
    go build -ldflags "-X main.commit=<sha> -X main.version=<version>" -o agent-box ./cmd/agent-box
    go test ./internal/procinfo/... ./internal/abduco/... ./internal/session/... ./internal/config/...

A binary you build yourself is not quarantined by Gatekeeper.

## Check the machine

    ./agent-box doctor

Every line is PASS, WARN or FAIL. The report contains no secrets, so it is safe
to paste into an issue.

## Run

    ./agent-box serve

With no config file, CuliBox listens on `127.0.0.1:8899` and prints a login
URL containing a login token (`#token=…`), generated on first start and kept in
`~/.local/share/agent-box/login.token`. Open the URL in a browser on the same Mac.

Closing the terminal tab stops CuliBox but not your sessions; run
`agent-box serve` again and they reattach. Only one CuliBox can serve a data
directory at a time.

## Configure

Everything CuliBox writes lives in one directory, `~/.local/share/agent-box`
(set `AGENT_BOX_HOME` to an absolute path to move it): its config, login token,
session records, profiles and analytics. Back it up, move it or delete it as a
unit. Claude Code's own files stay in `~/.claude`; CuliBox only reads them. The one
exception is the abduco socket directory, `/tmp/agent-box-<uid>-<hash>`, because macOS
caps a socket path at 103 bytes; it holds only live sockets.

Optional config: `~/.local/share/agent-box/config.json` (see `config.example.json`), or
pass `-config <path>`. When `AGENT_BOX_HOME` is unset, `~/.config/agent-box/config.json`
(or `$XDG_CONFIG_HOME/agent-box/config.json`) is still read as a fallback. A
`config.json` in the current directory is deliberately not read, so running CuliBox
inside a cloned repo never loads that repo's config. To reach
CuliBox through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

Both files sit in the data directory (default `~/.local/share/agent-box`).

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.

## Boxed tier (contained sessions)

A boxed session runs in a container inside CuliBox's **own** colima VM, which
lives under the data root (`<data root>/vm`). It never becomes your docker
context and never edits `~/.ssh/config`.

    brew install colima docker
    agent-box box setup        # once: creates the VM, builds the image natively (or pulls it when `box_image` is a ghcr digest), checks containment
    agent-box doctor           # the "boxed tier", "box image" and "containment" lines

- The VM starts on the first boxed spawn (expect 20–30 s) and keeps running.
  `agent-box box stop` frees its memory (default all your Mac's cores and half
  its RAM in whole GiB, at least 4 GiB; `box_vm_cpus`, `box_vm_memory_gib` in
  config.json). It refuses while any box is running, even
  an idle one; `agent-box box stop -force` stops the VM anyway and ends those
  sessions. `agent-box box status` shows the VM, the image and the boxes.
- **Upgrading with the size unset.** A VM created by an older CuliBox has 4
  CPUs / 4 GiB, and an existing VM keeps the size it was created with: while
  `box_vm_cpus` and `box_vm_memory_gib` are unset, CuliBox reads them back
  from the VM and nothing reads as changed. The host-derived default (every
  core, half the RAM) applies only when the VM is first set up, or after
  `agent-box box setup -recreate`, which deletes the VM, so its images and
  cache go too (box homes live on the host and stay) and the next setup
  rebuilds the image. Setting either key explicitly to a different size also
  changes the VM, and `agent-box doctor` then reports the change until you run
  `box setup -recreate`. Boxes size their memory from the VM (by default half
  of it, but at least `4g` or all of the VM if it has less), so the VM size is
  what bounds every box on a Mac.
- Workspaces must be under your home directory (the VM shares only that), and
  must not be, contain or sit inside the data root (it holds the VM's own
  settings and SSH key). They also must not be or contain the directory of the
  config file CuliBox reads, or of `profiles_path` (symlinks are followed to
  their targets); a folder below those is fine.
- A box cannot reach services listening on your Mac's 127.0.0.1, other than
  CuliBox's own credential broker and the egress proxy (see below). CuliBox
  checks this on every new box and refuses the box if the check fails.
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
- Clone mode (a box works on a private copy of a repo and its branches come
  home for review) runs here too, in the box VM. Keep the repo and its copy
  under your home directory; anything else is refused. `agent-box clone fetch`
  needs the VM running and never starts it. See
  [Clone mode](CLONE-MODE.md#on-macos). Not yet tested end to end on a Mac.
- Restart `agent-box serve` after any `box setup -recreate`. A running
  CuliBox keeps the VM settings it read when it started, so until the
  restart it sizes new boxes from the old VM, and a failed boxed spawn can
  still tell you to recreate. `agent-box doctor` reads the VM fresh each run.

### Every box

Every box, with or without a credential binding (an `aws` binding, named AWS
connections, a `github` binding or static tokens), gets a locked-down network,
as on Linux. It sits on an
internal Docker network inside the VM (`172.29.72.0/24`, gateway `172.29.72.1`),
with no route to the internet, no DNS, and no route to other boxes. The only
way out is a small relay container, `agentbox-relay-<instance>`, that runs on
the VM's own network and listens on `172.29.72.1` only. It forwards exactly
two ports from there to your Mac: CuliBox's egress proxy (port `egress_port`,
default 9072) and the credential broker (port `broker_port`, default 9071),
both of which listen on `127.0.0.1` on the Mac. A profile without a binding
reaches any public host on port 443 through the proxy ("open" in the steer
rail); a profile with bindings reaches only the hosts they allow. Rules in the
VM's firewall let the box subnet reach those two ports and nothing else, and
keep every other box off the relay. If those rules are missing or out of order,
agent-box refuses to start a boxed session and tells you that this VM predates
egress control for all boxes and to run `agent-box box setup -recreate`; it
checks them again on every boxed spawn.

Egress control for every macOS box was verified on a Mac (macOS 14, colima 0.8.1) on 2026-11-02: bound and unbound boxes are fenced, a box's proxy token is refused when used from another box, boxes cannot reach each other, and direct, DNS and Mac loopback connections are blocked.

What this means for an unbound profile, which used to have normal outbound
access on a Mac:

- No git over ssh and no other port than 443: the only way out is an HTTPS
  CONNECT through the proxy.
- No DNS for external names inside the box. The proxy resolves the name.
- A tool must honour `HTTPS_PROXY` (CuliBox sets it, and `NODE_USE_ENV_PROXY=1`
  for Node). A tool that ignores it cannot connect.
- Private and internal addresses are refused unless a binding's `egress` entry
  opts in for one exact host (see [Self-hosted services](STATIC-TOKENS.md#self-hosted-services)).

These limits are the same as on Linux and are not configurable.

- After upgrading, run `agent-box box setup -recreate` once. The rules live in
  the VM's provisioning, so an existing VM has none until it is recreated, and
  from this version that applies to every box, not only bound ones;
  `agent-box doctor` fails "this VM predates egress control for all boxes" and
  every boxed spawn is refused until you do.
  Like any recreate, it refuses while a box is running unless you add `-force`,
  and CuliBox must be restarted afterwards.
- The egress network uses the fixed subnet `172.29.72.0/24` inside the VM. An
  egress network left over from the previous build (an open one, or one with a
  different subnet) is refused with a message that suggests
  `docker network rm`. After an upgrade, run `agent-box box setup -recreate`
  instead: it deletes the VM, so its networks and rules come back fresh. Then
  restart CuliBox.
- The relay container has no restart policy. After a VM restart the next boxed
  session checks the firewall rules and starts it again, so that spawn can be
  a little slower than later ones.
- A box left from an older build, on the default network, is removed and
  recreated on the egress network when it is idle. If it still runs sessions,
  the next spawn into it is refused ("started before egress control") until you
  end them.
- A tool that ignores `HTTPS_PROXY` cannot connect, by design: there is no
  direct route to fall back on. A connection that fails with a timeout, not a
  clear error, is usually such a tool.
- Denials show in the steer rail's egress row for the session ("N denied since
  start") and, with host and time, in the `egress_audit` table of `audit.db` in
  the data directory.

### Token binding

The relay prepends a one-line header (PROXY protocol v1)
naming the box's own address on every connection it forwards. The proxy and
the broker on your Mac accept a connection only when that header is present and
names an address inside the box subnet other than the gateway, so a box's
proxy token works only from that box, as on Linux. Residual risk: a process on
your Mac running as your user can connect to `127.0.0.1` and write that header
itself. It already runs as you, inside your trust boundary, so this is not an
additional exposure.

The credential broker accepts a session's token only from the box that session
runs in. On macOS it compares the address in the relay's header with that box's
address on the egress network. The address comes from docker (the VM's docker),
is kept in a cache the egress proxy shares, and is looked up again at most every
10 seconds when a request does not match. If the broker cannot find out, it
refuses. On Linux each bound box has its own broker socket, so the box is known
from the socket the request arrives on.

A token presented from any other box, bound or unbound, is refused with the same
message as any other refusal and audited as `denied_box`. An unbound box on macOS
can open a connection to the broker, but its address never matches a bound
session's box, so its requests are refused. The broker caps the connections it
holds open, in total and for any one box, and closes idle ones. A refusal for the wrong box can take as long as the lookup timeout
(5 seconds) when docker is slow. That delay is a liveness hint only: a token
that does not verify already gets a different refusal (401) from a valid token
presented from the wrong box (403).

What stays: sessions that share a box (the same profile and binding) can read
each other's token; code planted into the owning box, for example through a
shared workspace, runs with that box's access; and a process on your Mac running
as your user.

### In-place guard

A box started in place sees the workspace root's `.git`, `.claude/` and any
top-level `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md` or `.mcp.json` read-only,
as on Linux, and the same repositories are refused (submodules, `core.worktree`,
a git config `include`, and so on). What it covers and what it does not is in
[Security model](SECURITY.md#known-limits).

- Measured on a Mac (colima 0.8.1, virtiofs, workspace under the home) for
  `.git`: writes, config edits, renaming `.git` or `hooks`, a new hook and a
  write through a symlink are refused in the box, and the Mac copy does not
  change. A read-only mount of only `.git/hooks` does not hold, which is why the
  whole `.git` is mounted. The other guarded names use the same read-only mount
  and have not been measured separately on a Mac yet.
- Not yet tested: whether a box can write a guarded file under another spelling
  of its name, such as `claude.md`, or a worktree's `.git` file as `.GIT`. A
  macOS volume ignores case by default, so the Mac treats that as the same
  file, while the read-only mount in the box covers only the one spelling.
  Until this is measured, treat those files as writable by the box. The guarded
  directories (`.git`, `.claude/`, the hooks directory) are expected to hold,
  but are not measured under another spelling either.
- After upgrading, a box that was started before this guard and still runs
  sessions is refused at the next spawn ("started without ... which this
  session needs") until you end those sessions. An idle one is recreated.

### Context paths

`context_paths` (see [Reading other repos](CONTEXT-PATHS.md)) makes the same
check as on Linux that the mounted path is the one that was checked, with the
identity read a different way. An inode number on the Mac is not the one the
box sees through the VM's file share, so on macOS the identity is read inside
the VM, by a short script run over one `colima ssh` per spawn with context
paths. It enters the parent directory (`cd -P`) and refuses unless that
directory's real path is the listed parent, so a symlink in any parent is
refused. It then reads the last component inside that directory with one
`lstat` (type and identity together) and refuses a symlink, a FIFO, a socket, a
device or anything else that is not a directory or regular file. It is a shell
script (`cd -P`, `pwd -P`, `stat`), not the descriptor walk Linux uses, and a
real directory moved into place before it reads is taken as the path you
listed.

- Measured on a Mac (colima 0.8.1, virtiofs): the device and inode the VM
  reports for a path are the ones the box's bind mount shows, and are stable
  across repeat reads.
- Tested only on Linux, with dash and GNU coreutils like the VM's: the script against a
  parent directory flipped between a directory and a symlink, and against the last
  component exchanged for a FIFO or a symlink, each in a loop.
- Not yet tested on a Mac: that `colima ssh` passes the script to the VM
  unchanged (if it does not, spawns with context paths are refused, not let
  through), the script on the VM itself, and that the identity stays stable
  across a VM restart.

## Behind a TLS-inspecting proxy

Some company networks (for example Zscaler or Netskope) decrypt HTTPS and
re-sign it with their own root CA. Your Mac trusts that root, but the box VM
and the boxes do not, so `box setup` fails with errors such as `curl: (60)`,
`SSL certificate problem` or `x509: certificate signed by unknown authority`.
When it sees one, `box setup` prints a `HINT:` that usually names the root CA your Mac
sees for the failing host and the export command below with the name filled in
(it leaves those lines out when it cannot reach the failing host itself).

1. Export the root CA to a PEM file. `<Root CN>` is the name in the hint, or
   the root certificate's name in Keychain Access (usually under the System
   keychain, Certificates):

       security find-certificate -c "<Root CN>" -p > ~/agent-box-ca.pem

2. Add it to `config.json`, with an absolute path:

       "extra_ca_certs": ["/Users/me/agent-box-ca.pem"]

   A config file replaces the built-in defaults. If you have none yet, create
   `~/.local/share/agent-box/config.json` (or `$AGENT_BOX_HOME/config.json`) with
   the defaults as well, or CuliBox refuses it for having no agents:

       {
         "bind_addr": "127.0.0.1:8899",
         "default_runtime": "host",
         "agents": [{"name": "claude", "bin": "claude"}],
         "extra_ca_certs": ["/Users/me/agent-box-ca.pem"]
       }

   The `HINT:` prints this file, and its path, when no config exists.

3. Apply it. The VM's provisioning changes, so the VM must be recreated.
   `-recreate` drops the VM's images, so the image is rebuilt (or pulled again):

       agent-box box setup -recreate

   It refuses while a box is running; add `-force` to end those sessions.

4. Restart CuliBox. `extra_ca_certs` is read at daemon start only, so a running
   `agent-box serve` keeps the old list until you restart it (your sessions
   survive the restart and reattach).

`box setup` and `agent-box doctor` then print one line per CA:

    PASS extra-ca: <subject> sha256:<fingerprint> expires <YYYY-MM-DD>

An expired CA (or one not yet valid) shows as `WARN extra-ca:` with the same
detail. It does not stop CuliBox, but trust through it will fail.

What the file may hold:

- Absolute paths to **certificate** PEM files only. A file with a private key
  in it is refused, so export the certificate, never the key.
- Each certificate must be a CA. A server certificate is refused.
- At most 32 certificates in total, and at most 1 MiB per file. Export the one
  root your proxy signs with, not your whole system trust store.

Trust a CA only if you trust its owner: **a trusted CA can read and modify all
TLS traffic inside the boxes.** Only whoever owns `config.json` can set it, and
`doctor` and `box setup` always list it, so it is never silent.

The CA is added to the image only while it builds and is removed before the
image is finished, so the image never trusts it: its effective trust store is
the stock one. (A locally built image still carries the public CA certificate in
an intermediate layer.) At run
time each box gets a read-only trust bundle (the image's own roots plus yours)
mounted over `/etc/ssl/certs/ca-certificates.crt`, and the usual variables
(`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS` and the like) point at it. If you change
the list while a box with live sessions is running, new sessions in that box
are refused until the old ones end (on macOS, `agent-box box stop` also ends them).

On Linux there is no VM. After a change, restart CuliBox; a box created after
that uses the new list, and the image build needs `agent-box box setup -rebuild`. A registry pull (a
`box_image` that is a `ghcr.io` digest) goes through the host's Docker daemon,
which uses the host's own trust store. CuliBox does not manage that: install
the proxy's root there (on Debian or Ubuntu, copy it into
`/usr/local/share/ca-certificates/` as a `.crt`, run `update-ca-certificates`
and restart docker). `box setup` prints this as a hint when a pull fails.
