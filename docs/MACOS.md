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
  opts in for one exact host (see the README, "Self-hosted services").

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

Not yet bound to the box: the credential broker's own session token, on macOS
and on Linux. On macOS the broker checks only that a request comes from the box
subnet; on Linux every bound box reaches it over the same socket. Neither checks
that the request comes from the box the token was issued to. Another box that
gets hold of a bound box's broker token (on Linux any bound box, on macOS any
box) can therefore ask the broker for that session's credentials (AWS role sessions, GitHub installation tokens, static
tokens). The exposure stays between boxes of one CuliBox, under one user; it is
not reachable from outside. Until it is fixed, do not run a profile you do not
trust next to a bound profile whose credentials are broader than its own.

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
