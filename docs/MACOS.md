# Running CuliBox on macOS

Sessions run as your user (host tier, **not contained**) unless you set up the
boxed tier, which runs them in a container inside CuliBox's own colima VM; see
[Boxed tier](#boxed-tier-contained-sessions) below.

## Install

Download the installer, read it, run it (use the release tag you want in place of `<tag>`):

    curl -fsSLO https://raw.githubusercontent.com/culiops/culibox/<tag>/scripts/setup.sh
    sh setup.sh

It installs a checksum-verified `culibox` into `~/.local/bin` and then runs
`culibox init`, which sets up everything else. It is a short POSIX `sh` script
that never uses `sudo` and installs only under the install folder; it downloads
and builds in a temporary folder that it removes, and with `--from-source` it may
run `brew install go` after an explicit `y`. Read it before you run it. Options: `--version vX.Y.Z` (default: the newest published
release), `--from-source` (build that release instead of downloading a binary,
see below) and `--prefix DIR` (default `~/.local/bin`, or `$PREFIX`).

**`setup.sh` refuses a release older than `v0.35.0`.** The script knows the first
version that ships `init` (`MIN_VERSION`, currently `v0.35.0`) and stops with a
clear message when the release is older, or when there is none.

With `gh` logged in, the script also uses it for the release lookup and downloads.

### What `setup.sh` does

Each step prints `==> <what>` before it runs. To do it by hand, follow the same steps:

1. **Checks the platform.** macOS or Linux, on amd64 (`x86_64`) or arm64. Anything else is refused.
2. **Picks the version.** `--version` as given (it must look like `vX.Y.Z`), else the newest published release (`gh release view`, or the GitHub releases API). A version older than `MIN_VERSION` is refused. The tag is resolved once and every later URL uses it.
3. **Stops early when that version is already installed.** It runs `~/.local/bin/culibox version` and, on a match, skips the download and goes straight to `init`.
4. **Gets the binary.** Into a temporary folder it downloads `culibox_<version>_<os>_<arch>.tar.gz` and `SHA256SUMS` from the release (with `gh release download` when `gh` is logged in, else `curl`). It finds the tarball's one line in `SHA256SUMS` and compares it with `shasum -a 256` (or `sha256sum`); a missing line, a repeated line or a mismatch installs nothing. It then checks the build attestation with `gh attestation verify <tarball> --repo culiops/culibox`; a rejected attestation, or a repository whose visibility it cannot read, installs nothing. With `gh` missing, logged out or without `gh attestation`, it prints "provenance not checked" and relies on the checksum. Last, it extracts `culibox` from the tarball. Nothing downloaded runs before its checksum matched.
   With `--from-source` it instead clones the tag (`git clone --depth 1 --branch <tag>`), needs a Go at least as new as the `go` line of that `go.mod`, and builds `CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=<tag> -X main.commit=<sha>" -o <temp>/culibox ./cmd/culibox` in the temporary folder, never in a source tree you keep. If Go is missing or too old it asks `Install go via brew? [y/N]`; it never installs Homebrew (it prints the official install line from <https://brew.sh> and stops).
5. **Installs.** Into `~/.local/bin/culibox` (creating the folder). If a different `culibox` was there, it first copies it to `culibox.prev` and prints `rollback: mv -f <path>.prev <path>`. It writes a side file and renames it, so a half-written binary is never in place. It prints the `export PATH=...` line to add to your shell's rc file when the folder is not on `PATH`.
6. **Runs `culibox init`.**

### What `culibox init` does

`culibox init` is a guided first-time setup. It needs a terminal (it asks questions): without one it lists what it would check and exits 2. Every step prints `== step N/8: <what>` and then exactly one of `done`, `already done: <why>`, `skipped: <why>` or `stopped: <what to do next>`. A stopped step exits 1 and gives the command to finish by hand; run `culibox init` again afterwards, and it continues from where the machine is. A run through all eight steps exits 0 when `culibox doctor` found no FAIL. Run it as `culibox init [-config <path>]`; `-config` must name a file that exists (leave it out and init creates `<data root>/config.json`).

1. **Dependencies.** Needs macOS 13 or newer, and `abduco`, `docker`, `colima` (macOS only) and `claude` on `PATH` (a binary you set in config, such as `docker_bin`, is looked up instead). For each missing one it asks, per package, ``Install <name> with `brew install ...`? [y/N]`` and runs only what you answered `y` to: `brew install abduco`, `brew install docker`, `brew install colima`, `brew install --cask claude-code`. By hand: run those. It never uses `sudo` and never installs Homebrew. On Linux it only prints the install commands.
2. **Data root.** `~/.local/share/culibox` (or `$CULIBOX_HOME`), created with mode 0700 when absent. By hand: `mkdir -p -m 700 ~/.local/share/culibox`.
3. **Workspace folder.** Asks for the folder where your agents work (default: `default_cwd` from your config). A box mounts the whole folder, so it must be a project folder: not the filesystem root, your home or a folder that contains it, and not CuliBox's own folders (the data root, the folder of the config file). It offers to create a folder that does not exist.
4. **VM size (macOS only).** Proposes the box VM's size and asks you to press Enter or type `<CPUs> <GiB>`: CPUs = half the Mac's cores (at least 2), RAM = a quarter of the Mac's RAM in whole GiB, between 4 and 8. This is inferred from the Mac's size, not measured. When `box_vm_cpus` and `box_vm_memory_gib` are both set in your config, it proposes those instead. When the VM already exists it prints `already done` and asks nothing (a size change on an existing VM needs `culibox box setup -recreate`, which init never runs). On Linux the step is skipped.
5. **Claude token.** Creates the [encrypted store](STORE.md) if there is none (on macOS the key goes in your login Keychain), offers to run `claude setup-token` (it opens a browser), and asks you to paste the token with the input hidden. The token goes into the store as `ACCT_CLAUDE_BOX_TOKEN` and an agent account named `claude-box` is created that uses it. The token is never printed and never written to `config.json`. By hand: `culibox store init`, `claude setup-token`, then in Settings → Agent accounts choose `+ new account`, name it `claude-box`, and paste the token on its row. An existing `claude-box` account is kept unless you answer yes to `Replace the token?`.
6. **Config.** Sets `default_runtime` to `box`, `default_cwd` to the workspace, `box_vm_cpus` and `box_vm_memory_gib` to the VM size, and `account` to `claude-box` on every `claude` agent that has none (adding a `claude` agent when there is none). A `claude` agent that already uses another account is left alone. It shows the file it would write (or a diff against yours; it rewrites your file with sorted keys and two-space indent) and asks before it writes, atomically and mode 0600, never through a symlink. It writes the config file in use: `-config <path>` if given (the file must already exist), else the one CuliBox finds, else `<data root>/config.json`, which init creates. By hand: put the same keys in that file (see [Configuration](CONFIG.md)).
7. **Box setup.** Runs `culibox box setup` (on macOS: creates the VM, builds the image, checks containment) unless the box is already ready, then, on macOS only, adds the workspace to the box VM's folders (`culibox box share add <workspace>`). The folder reaches the VM after `culibox box share apply`, which restarts the VM, or in Settings → VM folders.
8. **Doctor and login URL.** Runs `culibox doctor`, then prints how to start CuliBox (`culibox serve`) and the login URL with the login token. A FAIL in doctor makes the run stop with exit 1.

### From source (manual)

    brew install go abduco
    git clone https://github.com/culiops/culibox.git && cd culibox
    mkdir -p ~/.local/bin
    go build -ldflags "-X main.commit=$(git rev-parse --short HEAD) -X main.version=$(scripts/version.sh)" -o ~/.local/bin/culibox ./cmd/culibox
    go test ./internal/procinfo/... ./internal/abduco/... ./internal/session/... ./internal/config/...
    culibox init

Build into `~/.local/bin`, not into the source folder: a folder that holds a program CuliBox runs can only be shared with the box VM read-only. Add `~/.local/bin` to your `PATH` if it is not there.

A binary that `setup.sh` installs or that you build yourself is not quarantined by Gatekeeper.

## Check the machine

    culibox doctor

Every line is PASS, WARN or FAIL. The report contains no secrets, so it is safe
to paste into an issue.

## Run

    culibox serve

With no config file, CuliBox listens on `127.0.0.1:8899` and prints a login
URL containing a login token (`#token=…`), generated on first start and kept in
`~/.local/share/culibox/login.token`. Open the URL in a browser on the same Mac.

Closing the terminal tab stops CuliBox but not your sessions; run
`culibox serve` again and they reattach. Only one CuliBox can serve a data
directory at a time.

## Configure

Everything CuliBox writes lives in one directory, `~/.local/share/culibox`
(set `CULIBOX_HOME` to an absolute path to move it): its config, login token,
session records, profiles and analytics. Back it up, move it or delete it as a
unit. Claude Code's own files stay in `~/.claude`; CuliBox only reads them. The one
exception is the abduco socket directory, `/tmp/agent-box-<uid>-<hash>`, because macOS
caps a socket path at 103 bytes; it holds only live sockets. Its name keeps the `agent-box` prefix.

A Mac that has a box VM cannot move its data folder, because Lima and colima keep
absolute paths into it; see [From agent-box](UPGRADING.md#from-agent-box). The colima
profile is named `agent-box` and the Keychain item `agent-box-store` on every
install; nothing needs to be redone.

Optional config: `~/.local/share/culibox/config.json` (see `config.example.json`), or
pass `-config <path>`. When `CULIBOX_HOME` is unset, `~/.config/culibox/config.json`
(or `$XDG_CONFIG_HOME/culibox/config.json`) is read as a fallback, and so is
`~/.config/agent-box/config.json` (see [Configuration](CONFIG.md)). A
`config.json` in the current directory is deliberately not read, so running CuliBox
inside a cloned repo never loads that repo's config. To reach
CuliBox through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

These files sit in the data directory (default `~/.local/share/culibox`).

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.
- New relay secret (what the box VM's relay and CuliBox's proxy and broker
  prove to each other): delete `relay2.secret` and restart. The relay is
  replaced when CuliBox starts with the box VM running, or at the next boxed
  spawn, so connections open through it drop once.

## Boxed tier (contained sessions)

A boxed session runs in a container inside CuliBox's **own** colima VM, which
lives under the data root (`<data root>/vm`). It never becomes your docker
context and never edits `~/.ssh/config`.

`culibox init` runs these for you (steps 1 and 7); by hand:

    brew install colima docker
    culibox box setup        # once: creates the VM, builds the image natively (or pulls it when `box_image` is a ghcr digest), checks containment
    culibox doctor           # the "boxed tier", "box image" and "containment" lines

- The VM starts on the first boxed spawn (expect 20–30 s) and keeps running.
  `culibox box stop` frees its memory (default all your Mac's cores and half
  its RAM in whole GiB, at least 4 GiB; `box_vm_cpus`, `box_vm_memory_gib` in
  config.json; `culibox init` instead proposes a smaller size, half the cores
  and a quarter of the RAM, and writes it to config.json). It refuses while any box is running, even
  an idle one; `culibox box stop -force` stops the VM anyway and ends those
  sessions. `culibox box status` shows the VM, the image and the boxes.
- **VM size.** An existing VM keeps the size it was created with: while
  `box_vm_cpus` and `box_vm_memory_gib` are unset, CuliBox reads them back
  from the VM and nothing reads as changed. The host-derived default (every
  core, half the RAM) applies only when the VM is first set up, or after
  `culibox box setup -recreate`, which deletes the VM, so its images and
  cache go too (box homes live on the host and stay) and the next setup
  rebuilds the image. Setting either key explicitly to a different size also
  changes the VM, and `culibox doctor` then reports the change until you run
  `box setup -recreate`. Boxes size their memory from the VM (by default half
  of it, but at least `4g` or all of the VM if it has less), so the VM size is
  what bounds every box on a Mac.
- Workspaces must be inside a folder the VM shares read & write (see
  [VM folders](#vm-folders); a read-only folder is refused with its own error),
  and must not be, contain or sit inside the data root (it holds the VM's own
  settings and SSH key). They also must not be or contain the directory of the
  config file CuliBox reads, or of `profiles_path` (symlinks are followed to
  their targets); a folder below those is fine.
- A box cannot reach services listening on your Mac's 127.0.0.1, other than
  CuliBox's own credential broker and the egress proxy (see below). CuliBox
  checks this on every new box and refuses the box if the check fails.
- Claude Code keeps its macOS login in the Keychain, which a box cannot read.
  `culibox init` creates a long-lived token with `claude setup-token`, keeps it
  in the encrypted store and links it to an agent account named `claude-box`
  (see [What `culibox init` does](#what-culibox-init-does), step 5). To do it by hand, or to
  change the token later, paste a `claude setup-token` token on the account's
  row in Settings → Agent accounts; it needs no `secret_sources` entry.
- Requirements: macOS 13+, Apple Silicon (Intel Macs untested), colima 0.8+.
- Not yet tested: company VPN/split-DNS from inside a box.
- File-heavy work (`npm install`, large builds) is slower boxed than on the host
  tier: the workspace crosses a VM file share (virtiofs).
- If doctor warns about the VM's mount type, `culibox box setup -recreate`
  deletes and recreates the VM. Like `box stop`, it refuses while any box is
  running, and also when the VM is not running (its boxes cannot be checked
  then); add `-force` to go ahead and end any sessions in it.
- Clone mode (a box works on a private copy of a repo and its branches come
  home for review) runs here too, in the box VM. The repo must be inside a VM
  folder and its copy inside a read-write one; anything else is refused. `culibox clone fetch`
  needs the VM running and never starts it. See
  [Clone mode](CLONE-MODE.md#on-macos). Tested end to end on a Mac (macOS 27.2.1,
  colima 0.10.3): work comes back as a `box/<branch>` and a refresh fast-forwards the clone.
- Restart `culibox serve` after any `box setup -recreate`. A running
  CuliBox keeps the VM settings it read when it started, so until the
  restart it sizes new boxes from the old VM, and a failed boxed spawn can
  still tell you to recreate. `culibox doctor` reads the VM fresh each run.

### VM folders

Once the folder list is applied, the box VM does not share your home folder.
It sees only the folders on its list, each read-write or read-only,
plus four parts of CuliBox's own data folder: `boxstate` (read-write) and
`bundle`, `broker` and `ca` (read-only). Nothing else in the data folder is
visible to the VM, so the config file, credentials and the VM's own
`colima.yaml` are out of reach.

The list is kept in `vm-shares.json` in the data folder, written only by
CuliBox (it is not a `config.json` key; see [Configuration](CONFIG.md#vm-folders)).
On the first run CuliBox builds it from what your profiles already use: their
workspaces, clone sources (read-only) and clone parents, context paths
(read-only), and Claude's project memory folders (read-only), plus the folders
boxed sessions have already run in.

- Manage it in Settings › VM folders, or with `culibox box share list`,
  `add <dir> [-ro]`, `rm <dir>` and `apply [-force]`. A change is saved at
  once but reaches the VM only when you apply it.
- Applying stops the VM, rewrites its folder settings and starts it again. It
  takes about 20 seconds, ends running boxes (so it refuses while any run,
  unless you use `-force`; it also refuses when the VM reads as not running,
  because running boxes cannot be ruled out, and needs `-force` then too) and keeps the images and the cache. It never
  deletes the VM. `culibox box setup` applies a folder-list-only change the
  same way.
- CuliBox refuses a folder that is your home folder or contains it, that
  overlaps its data folder, that holds CuliBox's own config, profiles or
  bundle source directory (a share may not be, contain or, for the data folder
  and the bundle source, sit inside one; symlinks are followed), or that holds
  keys or sockets: `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`, `~/.docker`, `~/.colima`, `~/.lima`, `~/Library`,
  `~/.config/gcloud`, `~/.config/gh`, `~/.azure`, `~/.terraform.d`,
  `~/.password-store`, `~/.config/op`, and `~/.claude` apart from a project's
  `memory` folder. That folder, and anything inside it, can only be shared
  read-only: your own Claude Code loads what is in it. A folder that is or
  holds the folder of a program CuliBox runs (docker, colima, limactl or
  CuliBox itself, symlinks followed) cannot be shared read & write, because a
  box that could write there could replace the program; read-only is fine.
  If the programs live in `/opt/homebrew` or `/usr/local`, those folders are
  read-only at most, and so is a project folder you built `culibox` in.
  Folders on the list
  never contain one another: adding a parent absorbs the entries inside it, and a read-write folder cannot sit
  inside a read-only one.
- A boxed spawn checks the folders against what the running VM actually
  has, not against your saved list. A folder you added but have not applied is
  refused, with an offer to share it.
- A VM whose folder list was never applied still shares your whole home
  folder. Nothing breaks: `culibox doctor` ("box VM folders") and Settings
  warn about it, and the home share goes away when you apply the list.
- Switching a folder between read-write and read-only takes effect on one apply: after the
  start CuliBox remounts any folder the VM still shows at its old mode, then checks the
  folder list again. A missing or extra folder is never remounted, and a remount that
  fails or does not hold fails the apply.
- What a VM escape can reach: only the folders on the list (read-write or
  read-only as listed) and CuliBox's four data parts (`boxstate`
  read-write; `bundle`, `broker` and `ca` read-only). Everything inside a
  listed folder is reachable, so keep secrets out of shared folders. CuliBox
  refuses to share its own config, profiles and data directories and the
  known credential folders above.

### Every box

Every box, with or without a credential binding (AWS connections, a `github` binding or static tokens), gets a locked-down network,
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
keep every other box off the relay. If those rules are missing or out of order
(for example a VM set up for other ports),
CuliBox refuses to start a boxed session and tells you to run
`culibox box setup -recreate`; it checks them again on every boxed spawn.

Egress control for every macOS box was verified on a Mac (macOS 14, colima 0.8.1) on 2026-11-02: bound and unbound boxes are fenced, a box's proxy token is refused when used from another box, boxes cannot reach each other, and direct, DNS and Mac loopback connections are blocked.

What this means for an unbound profile:

- No git over ssh and no other port than 443: the only way out is an HTTPS
  CONNECT through the proxy.
- No DNS for external names inside the box. The proxy resolves the name.
- A tool must honour `HTTPS_PROXY` (CuliBox sets it, and `NODE_USE_ENV_PROXY=1`
  for Node). A tool that ignores it cannot connect.
- Private and internal addresses are refused unless a binding's `egress` entry
  opts in for one exact host (see [Self-hosted services](STATIC-TOKENS.md#self-hosted-services)).

These limits are the same as on Linux and are not configurable.

- Run `culibox box setup -recreate` when doctor asks for it (what an upgrade
  keeps and how to go back: [Upgrading](UPGRADING.md)). The rules live in the
  VM's provisioning, so an existing VM keeps the rules it was set up with until
  it is recreated. When they differ from what this CuliBox writes, `culibox doctor`
  fails "box VM config" (and "box VM firewall" while the VM runs) and every boxed
  spawn is refused. Like any recreate, it refuses while a box is running unless
  you add `-force`, and CuliBox must be restarted afterwards.
- The egress network uses the fixed subnet `172.29.72.0/24` inside the VM. A
  network of that name that is open, that lets boxes reach each other, or that has
  another subnet or gateway is refused, with a message that suggests
  `docker network rm`. `culibox box setup -recreate` deletes the VM, so its
  networks and rules come back fresh. Then restart CuliBox.
- The relay container has no restart policy. After a VM restart the next boxed
  session checks the firewall rules and starts it again, so that spawn can be
  a little slower than later ones.
- A box that fails its egress check is removed and recreated on the egress
  network when it is idle. If it still runs sessions, the next spawn into it is
  refused ("started before egress control (or failed its egress check)") until you
  end them.
- A tool that ignores `HTTPS_PROXY` cannot connect, by design: there is no
  direct route to fall back on. A connection that fails with a timeout, not a
  clear error, is usually such a tool.
- Denials show in the steer rail's egress row for the session ("N denied since
  start") and, with host and time, in the `egress_audit` table of `audit.db` in
  the data directory.

### Token binding

The relay and CuliBox's proxy and broker on your Mac first prove to each other
that they hold the same secret, without sending it: each side answers a fresh
random challenge from the other with a keyed hash of it (HMAC-SHA256). Until the
listener has answered correctly the relay sends nothing else, no header and no
byte from the box, and it gives up after 5 seconds. Then it sends a header
(PROXY protocol v1) naming the box's own address. The proxy and the broker
accept a connection only when the relay's answer is right and the header names
an address inside the box subnet other than the gateway, so a box's token is
refused when another box presents it. Any other connection is closed without a
response, and CuliBox's log counts such refusals at most once a minute.

The secret is 32 random bytes in `relay2.secret` in the data directory, readable
only by you. CuliBox creates it on first start, keeps it across restarts, and
hands it to the relay container through its environment, never on a command
line. Another user account on the same Mac therefore cannot write a header the
proxy or the broker accepts, and a process that
takes one of CuliBox's ports while CuliBox is stopped or restarting gets only
the relay's random challenge: no header, no token and nothing a box sent.
Measured on a Mac (macOS 27.2.1, colima 0.10.3): a second macOS account could not
read `relay2.secret`, its forged header lines were closed, and an impostor on the
broker's port received only the challenge.

This is still weaker than on Linux, where the box is known from the socket a
request arrives on. A process running as you can read the secret, and with a
token taken from a box its requests then count as that box's; such a process is
inside your trust boundary anyway. Anything that can read your Mac's loopback
traffic (root, or any account allowed to capture packets, for example through
Wireshark's ChmodBPF) sees the session tokens and everything sent after the
handshake in cleartext. It does not see the secret, and what it sees does not
let it open a connection that the proxy or the broker take for the relay's.
Loopback traffic is not encrypted, though, so treat such an account like root:
it may be able to tamper with a connection that is already open. A process under
any account can still open connections that take up the proxy's and the
broker's shared connection slots until they time out; that can delay or drop
boxes' connections (the relay gives up on one after 5 seconds), not impersonate
them.

The handshake proofs bind the port, not the address. That is enough only while
the box VM's host agent (lima, under colima) dials CuliBox at `127.0.0.1`. If it
dialed another address, a process bound to that address could sit between the
relay and CuliBox's listener, and the listener would answer the relay's
challenge through it. Such a process would see the session tokens and the rest
of the stream, and could alter it. Only protecting the stream after the
handshake would close that, and CuliBox does not. On a Mac it was measured that
the host agent's connections to the broker and egress ports come from `127.0.0.1`
only.

CuliBox checks the VM's firewall at every boxed spawn: the relay's INPUT rules,
the gateway DROP, and that no DOCKER-USER rule passes TCP (so no container on the
VM's default Docker network, outside the egress network, reaches the broker's
port without the relay). If the check fails, `culibox doctor` fails "box VM
firewall" and boxed spawns are refused until you run `culibox box setup -recreate`.

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
session's box, so its requests are refused. The broker and the egress proxy each cap the
connections they hold open, in total and for any one box, and close idle ones. A refusal for the wrong box can take as long as the lookup timeout
(5 seconds) when docker is slow. That delay is a liveness hint only: a token
that does not verify already gets a different refusal (401) from a valid token
presented from the wrong box (403).

What stays: sessions that share a box (the same profile and binding) can read
each other's token; code planted into the owning box, for example through a
shared workspace, runs with that box's access; a process running as you on your
Mac that holds a session's token, and an account that can read loopback traffic
(see above); a process that receives the relay's connection on an address
other than `127.0.0.1` could sit in the middle, because the proofs bind the port
and not the address (see above; the host agent was measured to dial
`127.0.0.1`); a container on the VM's host network or another process in the VM,
which reaches the Mac's loopback through the VM's own OUTPUT path (the DOCKER-USER
rules do not apply there) and so can connect to the broker's port, but cannot pass
the handshake without the relay secret; and while CuliBox does not hold its ports, boxes cannot reach the
proxy or the broker.

While CuliBox is not listening on its ports (stopped, restarting, or its
listeners refused to start, for example because `relay2.secret` could not be
loaded or a port could not be bound), boxes cannot reach the proxy or the
broker, and whatever else holds those ports gets only the relay's challenge. If
another process holds one of the ports, CuliBox's log or the spawn refusal says
so and names the command that finds it (`sudo lsof -nP -iTCP:<port> -sTCP:LISTEN`;
without `sudo`, `lsof` does not list another account's process);
the broker stays off until CuliBox restarts, and the proxy tries again at the
next boxed spawn.

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
- Another spelling of a guarded file's name (measured on a Mac, 2026-11-10): a
  macOS volume ignores case by default, so `claude.md`, `.MCP.JSON` or `.GIT`
  is the same Mac file as `CLAUDE.md`, `.mcp.json` or a worktree's `.git` file,
  while the read-only mount in the box covers only the one spelling. Through
  another spelling the box could change the Mac file. The guarded directories
  (`.git/`, `.claude/`, the hooks directory) hold under every spelling.
- So the guarded files are also **locked on the Mac while the box runs**. On a
  case-insensitive volume, before a box started in place starts, CuliBox sets
  the user-immutable flag (`chflags uchg`) on each guarded single file there
  is: `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `.mcp.json` and a
  worktree's `.git` file (never a directory). Measured on a Mac (2026-11-11):
  with the flag set, nothing in the box or the VM can write, truncate, remove,
  rename, replace, chmod, touch or hard-link the file, or clear the flag,
  through any spelling. A case-sensitive volume is not locked: it does not need
  it.
  - When: from before the box starts until the box is gone. The box is removed
    as soon as its last session ends (instead of idling for `box_idle_ttl_seconds`),
    because what a session started in it can outlive the session; the files are
    unlocked right after. Several sessions or boxes in the same folder share the
    lock, and it is lifted when the last one is gone. The lock is the file's,
    so two spellings of one folder (`~/code/Repo` and `~/code/repo`, two boxes)
    share it too.
  - If a file cannot be locked (you do not own it, or it cannot be opened), the
    spawn is refused and the message names the file. Fix the file, remove it,
    or use a clone-mode profile. A volume that has no such flag (exFAT, a
    network share) is refused with that reason: use a clone-mode profile there.
  - A box runs in place only over locks CuliBox set itself. If a guarded file
    is locked already and this CuliBox did not lock it (you did, another
    CuliBox instance with its own data folder did, or an earlier run whose
    lock records were lost), the spawn is refused and the message names the
    file. CuliBox never clears a flag it did not set. So two CuliBox instances
    on one Mac never box the same folder in place at once: the second is
    refused until the first's box is gone (its lock would otherwise be lifted
    under the second's box). End the other box, unlock a file you locked
    yourself with `chflags nouchg`, or use a clone-mode profile.
  - The cost: while the session runs you cannot edit those files on the Mac,
    and `git checkout` (or a branch switch) exits 0 but prints `unable to
    unlink old 'CLAUDE.md'` and leaves the file with its old content, shown as
    modified. End the session first, or use a clone-mode profile.
  - Unlocking a file by hand (`chflags nouchg CLAUDE.md`) while the session
    runs reopens the gap: the box can then change it through another spelling.
  - The locks are recorded in `inplace-locks.json` in the data folder, so a
    crash or a Mac restart does not leave them behind: CuliBox lifts the ones
    whose box is gone when it starts (and when the box VM starts). `culibox
    doctor` lists the files locked now and any left behind, with the
    `chflags nouchg` line to lift one by hand. The session's side panel says
    which files are locked. If you move or rename the folder while a session
    runs, CuliBox no longer finds the files there: unlock them by hand
    (`ls -lO` shows `uchg`). A file on a disk that came back with another
    device number (an external disk after a restart) is not unlocked on a
    guess either: doctor lists it with the line to unlock it. If the records
    file cannot be read, CuliBox keeps it as `inplace-locks.json.unreadable`
    and starts with none: a file it had locked stays locked, and a spawn in
    its folder is refused naming it, until you unlock it. Records written by
    a newer CuliBox are left as they are, and every in-place box that needs a
    lock is refused until you run that CuliBox, or stop CuliBox, restore the
    pre-upgrade backup and start it again (it reads the records only when it
    starts).
    End the in-place sessions under the newer CuliBox before you roll back,
    and afterwards check that no guarded file is left locked (steps 0 and 5 of
    [Rolling back](UPGRADING.md#rolling-back)).

### Context paths

`context_paths` (see [Reading other repos](CONTEXT-PATHS.md)) makes the same
check as on Linux that the mounted path is the one that was checked, with the
identity read a different way. An inode number on the Mac is not the one the
box sees through the VM's file share, so on macOS the identity is read inside
the VM, by a short script run over one `colima ssh` per boxed spawn (the same
script reads the workspace, see [Workspace identity](#workspace-identity)). It
enters the parent directory (`cd -P`) and refuses unless that directory's real
path is the listed parent, so a symlink in any parent is refused. It then
reads the last component inside that directory with one `lstat` (type and
identity together) and refuses a symlink, a FIFO, a socket, a device or anything
else that is not a directory or regular file. It is a shell script (`cd -P`,
`pwd -P`, `stat`), not the descriptor walk Linux uses, and a real directory
moved into place before it reads is taken as the path you listed.

- Measured on a Mac (colima 0.8.1, virtiofs): the device and inode the VM
  reports for a path are the ones the box's bind mount shows, and are stable
  across repeat reads.
- Tested only on Linux, with dash and GNU coreutils like the VM's: the script against a
  parent directory flipped between a directory and a symlink, and against the last
  component exchanged for a FIFO or a symlink, each in a loop.
- Measured on a Mac (macOS 27.2.1, colima 0.10.3, 2026-11-10): `colima ssh` passes
  the script to the VM unchanged (if it did not, every boxed spawn would be refused,
  not let through), the script works on the VM itself, and the device and inode it
  reports for a listed directory are the same before and after a VM restart. The
  read adds 56 to 76 ms (median) to a spawn.

### Workspace identity

When a box starts, CuliBox checks inside it that the workspace mount shows the
folder that passed the checks, and, for a box it creates, that each in-place
guard shows the file or directory it was derived from (see
[Security model](SECURITY.md#known-limits)). As with context paths, the
identity is read inside the VM, because an inode number on the Mac is not the
one the box sees. The workspace is read by the context-path script, in the same
single `colima ssh` per spawn, so every boxed spawn makes one, with or
without context paths. The comparison inside the box is one `docker exec`, so a
plain boxed spawn, without context paths, makes that too.

- The host's own reading of the workspace folder, the one the folder opened to
  derive the guards is compared with, follows no link in any part of the path
  (one `open` with `O_NOFOLLOW_ANY`, macOS 11+), so a folder above the workspace
  swapped for a link between resolving the folder and that reading is refused.
  It needs read permission on the workspace folder itself.
- A real folder moved into place before the VM reads it is taken as the path you
  listed: the VM read says what is at the path when it runs, not what was
  there when the folder passed the checks. Only a link is caught. A change after
  that read, up to the box's own mount, is caught by the comparison inside the
  box.
- A box that failed a check and could not be removed is never reused as it is:
  the next spawn removes it again first, and is refused while it cannot.
- A workspace reached by another spelling of its name (another case, or the
  composed and decomposed forms of an accented letter) started on a Mac with the
  identity equal to that of the real folder, and was not refused (measured on
  2026-11-10, colima 0.10.3). The script compares the path as given with the
  real path, so a spelling it does not accept is refused, not let through: open
  the folder by its exact name, as `ls` shows it, if that happens.
- Measured on a Mac (M2 mini, colima 0.10.3, 2026-11-10): the workspace read and
  the comparison add a median of 0.32 s to a plain boxed spawn (Linux: 0.11 s).
  The read is bounded at 10 s, so a wedged `colima ssh` stalls other spawns for
  that long at most; a read that takes longer is refused, and spawning again
  retries it.
- Measured on a Mac (macOS 27.2.1, colima 0.10.3, 2026-11-10): the identity the VM
  reports for the workspace and for the guards is the one the box shows. On APFS
  (2026-11-14), a workspace under a symlinked parent starts, and when a parent was
  swapped for a link while spawns ran, 7 of 8 spawns gave a box whose folder
  matched the identity checked, and 1 was refused. Not measured: that the `O_NOFOLLOW_ANY` open
  refuses a link in each position of the path.
- Tested only on Linux, against a stand-in for the VM read: the order of the
  read and of the box's creation, that the workspace is first in it, and the
  refusal and removal of a box whose mount changed.

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

       security find-certificate -c "<Root CN>" -p > ~/culibox-ca.pem

2. Add it to `config.json`, with an absolute path:

       "extra_ca_certs": ["/Users/me/culibox-ca.pem"]

   A config file replaces the built-in defaults. If you have none yet, create
   `~/.local/share/culibox/config.json` (or `$CULIBOX_HOME/config.json`) with
   the defaults as well, or CuliBox refuses it for having no agents:

       {
         "bind_addr": "127.0.0.1:8899",
         "default_runtime": "host",
         "agents": [{"name": "claude", "bin": "claude"}],
         "extra_ca_certs": ["/Users/me/culibox-ca.pem"]
       }

   The `HINT:` prints this file, and its path, when no config exists.

3. Apply it. The VM's provisioning changes, so the VM must be recreated.
   `-recreate` drops the VM's images, so the image is rebuilt (or pulled again):

       culibox box setup -recreate

   It refuses while a box is running; add `-force` to end those sessions.

4. Restart CuliBox. `extra_ca_certs` is read at daemon start only, so a running
   `culibox serve` keeps the old list until you restart it (your sessions
   survive the restart and reattach).

`box setup` and `culibox doctor` then print one line per CA:

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
are refused until the old ones end (on macOS, `culibox box stop` also ends them).

On Linux there is no VM. After a change, restart CuliBox; a box created after
that uses the new list, and the image build needs `culibox box setup -rebuild`. A registry pull (a
`box_image` that is a `ghcr.io` digest) goes through the host's Docker daemon,
which uses the host's own trust store. CuliBox does not manage that: install
the proxy's root there (on Debian or Ubuntu, copy it into
`/usr/local/share/ca-certificates/` as a `.crt`, run `update-ca-certificates`
and restart docker). `box setup` prints this as a hint when a pull fails.

## Release verification

A release's darwin tarballs are built on Linux and are not run on a Mac by CI, so
each release starts with `⚠️ NOT yet verified on macOS` in its Platform status
block. The maintainers then check the published files on an Apple Silicon Mac and
record the result on the release, which rewrites the two darwin lines. The lines
you can see on a release (also explained in the [README](../README.md)):

- **verified** (`✅`): both arches passed their tests and version check, arm64
  native and amd64 under Rosetta 2, and at least one manual check passed. No
  manual check failed.
- **partial** (`⚠️`): nothing failed, but the record is not enough to say
  verified: no manual check passed (none given, or all not applicable), or a
  darwin tarball or an arch result is missing.
- **failed** (`❌`): an arch's tests or version check failed, or a manual check
  failed. Do not use that build.

amd64 is checked under Rosetta 2 on Apple Silicon, not on Intel hardware, and the
record says so.

### Running the check (maintainers)

`scripts/macos-verify.sh <tag>` does the check. Run it on an Apple Silicon Mac (it
refuses anything else, and a Rosetta shell counts as Intel). You need:

- `gh`, logged in with write access to the release. Whether the download, view and edit steps work on a draft release is untested.
- `git`, Go, and `cc` plus `make` (the script builds the pinned abduco from source).
- Rosetta 2: `softwareupdate --install-rosetta --agree-to-license`.

What it checks, in order:

1. Downloads the two darwin tarballs and `SHA256SUMS` from the release and checks
   the sums. Before any release code or binary runs it also checks that the tagged
   commit is reachable from `main` and, once the repository is public, that each
   tarball has a build attestation from the release workflow for this tag and
   commit. A mismatch or a failed check stops the script.
2. Clones the tag, builds abduco, and runs `go test ./...` natively (arm64) and
   with `GOARCH=amd64`, which runs under Rosetta 2. A failing test run does not
   stop the script; it is recorded as failed.
3. Runs each extracted binary's `version` (the amd64 one under `arch -x86_64`)
   and requires exactly `culibox <tag> (<commit of the clone>)`.
4. Asks for the results of the manual checks, one per line as
   `ID=pass|fail|na [note]`, and a blank line to finish. The checks are the
   maintainers' own list of things to try by hand on a Mac. Nothing defaults to pass.
5. Builds `macos-verification.json` and uploads it to the release, then rewrites
   the two darwin lines of the Platform status block in the release notes.

Exit codes: `0` verified; `3` the record was written but the verdict is partial or
failed (the release now says so); `1` any other failure (nothing is uploaded unless
the message says so); `2` usage. On a non-zero exit the work directory keeps its
logs and downloads, and the script prints its path.

Run it again for the same tag and the new run is added to `runs[]` in
`macos-verification.json`; the asset on the release is replaced, so earlier runs
stay in the file. If the tag now points at a different commit than an earlier run
recorded, the script stops instead of mixing runs for different files. If the
upload worked but the notes edit did not, run it again.
