# Running Culibox on macOS

Sessions run as your user (host tier, **not contained**) unless you set up the
boxed tier, which runs them in a container inside Culibox's own colima VM; see
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

With no config file, Culibox listens on `127.0.0.1:8899` and prints a login
URL containing a login token (`#token=…`), generated on first start and kept in
`~/.local/share/agent-box/login.token`. Open the URL in a browser on the same Mac.

Closing the terminal tab stops Culibox but not your sessions; run
`agent-box serve` again and they reattach. Only one Culibox can serve a data
directory at a time.

## Configure

Everything Culibox writes lives in one directory, `~/.local/share/agent-box`
(set `AGENT_BOX_HOME` to an absolute path to move it): its config, login token,
session records, profiles and analytics. Back it up, move it or delete it as a
unit. Claude Code's own files stay in `~/.claude`; Culibox only reads them. The one
exception is the abduco socket directory, `/tmp/agent-box-<uid>-<hash>`, because macOS
caps a socket path at 103 bytes; it holds only live sockets.

Optional config: `~/.local/share/agent-box/config.json` (see `config.example.json`), or
pass `-config <path>`. When `AGENT_BOX_HOME` is unset, `~/.config/agent-box/config.json`
(or `$XDG_CONFIG_HOME/agent-box/config.json`) is still read as a fallback. A
`config.json` in the current directory is deliberately not read, so running Culibox
inside a cloned repo never loads that repo's config. To reach
Culibox through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

Both files sit in the data directory (default `~/.local/share/agent-box`).

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.

## Boxed tier (contained sessions)

A boxed session runs in a container inside Culibox's **own** colima VM, which
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
- **Upgrading with the size unset.** A VM created by an older Culibox has 4
  CPUs / 4 GiB, and an existing VM keeps the size it was created with: while
  `box_vm_cpus` and `box_vm_memory_gib` are unset, Culibox reads them back
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
  config file Culibox reads, or of `profiles_path` (symlinks are followed to
  their targets); a folder below those is fine.
- A box cannot reach services listening on your Mac's 127.0.0.1, other than
  Culibox's own credential broker and, for boxes with a binding, the egress
  proxy (see below). Culibox checks this on every new box and refuses the box
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
- Restart `agent-box serve` after any `box setup -recreate`. A running
  Culibox keeps the VM settings it read when it started, so until the
  restart it sizes new boxes from the old VM, and a failed boxed spawn can
  still tell you to recreate. `agent-box doctor` reads the VM fresh each run.

### Boxes with credential bindings

A profile with any binding (an `aws` binding, named AWS connections, a `github`
binding or static tokens) gets a locked-down network. Its box can reach only the
credential broker and Culibox's egress proxy on your Mac (the proxy listens
on `127.0.0.1`, port `egress_port`, default 9072; boxes call it
`host.lima.internal`), and the proxy lets through only the hosts the
profile's bindings allow. Everything else is dropped by rules inside the VM:
direct connections, DNS, and other boxes. Profiles without a binding are
unchanged and keep normal outbound access.

Egress control for macOS boxes is new and has not yet been verified on a Mac.

- After upgrading, run `agent-box box setup -recreate` once. The rules live in
  the VM's provisioning, so an existing VM has none until it is recreated;
  `agent-box doctor` warns "box VM config changed since setup" until you do.
  Like any recreate, it refuses while a box is running unless you add `-force`,
  and Culibox must be restarted afterwards.
- The bound network uses the fixed subnet `172.29.72.0/24` inside the VM.
- A tool that ignores `HTTPS_PROXY` cannot connect, by design: there is no
  direct route to fall back on. A connection that fails with a timeout, not a
  clear error, is usually such a tool.
- Denials show in the steer rail's egress row for the session ("N denied since
  start") and, with host and time, in the `egress_audit` table of `audit.db` in
  the data directory.

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
   the defaults as well, or Culibox refuses it for having no agents:

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

4. Restart Culibox. `extra_ca_certs` is read at daemon start only, so a running
   `agent-box serve` keeps the old list until you restart it (your sessions
   survive the restart and reattach).

`box setup` and `agent-box doctor` then print one line per CA:

    PASS extra-ca: <subject> sha256:<fingerprint> expires <YYYY-MM-DD>

An expired CA (or one not yet valid) shows as `WARN extra-ca:` with the same
detail. It does not stop Culibox, but trust through it will fail.

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

On Linux there is no VM. After a change, restart Culibox; a box created after
that uses the new list, and the image build needs `agent-box box setup -rebuild`. A registry pull (a
`box_image` that is a `ghcr.io` digest) goes through the host's Docker daemon,
which uses the host's own trust store. Culibox does not manage that: install
the proxy's root there (on Debian or Ubuntu, copy it into
`/usr/local/share/ca-certificates/` as a `.crt`, run `update-ca-certificates`
and restart docker). `box setup` prints this as a hint when a pull fails.
