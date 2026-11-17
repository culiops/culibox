# Install CuliBox

The short version is the [Quick start in the README](../README.md#quick-start). This page has the details: what CuliBox needs, what the installer does, installing a release tarball by hand, building from source, and how to tell whether a macOS build is verified.

## Requirements

- Go 1.26.11 or newer (see `go.mod`) to build.
- Linux or macOS. macOS needs 13+ on Apple Silicon (Intel is untested) for the boxed tier.
- [abduco](https://www.brain-dump.org/projects/abduco/). It is required; the daemon will not start without it. Release tarballs from `v0.36.0` include it, and `setup.sh` installs it next to `culibox`. CuliBox uses the `abduco` in the same folder as the `culibox` binary first, then one on `PATH` (see `abduco_bin` in [docs/CONFIG.md](CONFIG.md)). Building from source, you install it yourself: see [From source (manual)](#from-source-manual).
- For the boxed tier: Docker on Linux, or `colima` and `docker` on macOS. On Linux the docker daemon must run on the same machine and see host files as they are (a local Docker Engine). A VM-backed or remote daemon (for example Docker Desktop for Linux or a remote docker context) is not supported for boxed sessions: CuliBox refuses to start a box there, because it cannot confirm the box got the folder that was checked.
- The agent CLI you want to run. Claude Code (`claude`) is the supported agent. Other terminal programs can be configured as agents (a shell, for example), but status, usage and reply features are Claude Code only.
- Node.js, only to run the browser-side tests in development.

## Install with setup.sh

Download the installer from the newest release, read it, run it:

```sh
curl -fsSLO https://github.com/culiops/culibox/releases/latest/download/setup.sh
sh setup.sh
```

Every release from `v0.36.0` attaches `setup.sh`, and its `SHA256SUMS` lists it. To check the file you downloaded, fetch the newest release's `SHA256SUMS` and compare (on macOS, use `shasum -a 256 -c -`):

```sh
curl -fsSLO https://github.com/culiops/culibox/releases/latest/download/SHA256SUMS
grep ' setup.sh$' SHA256SUMS | sha256sum -c -
```

Without `--version`, the installer installs the newest published release, whichever release its own copy came from.

`setup.sh` is a short POSIX `sh` script. It installs a checksum-verified `culibox` into `~/.local/bin` (it never uses `sudo` and installs only under that folder; it downloads and builds in a temporary folder that it removes, and with `--from-source` on macOS it may run `brew install go` after an explicit `y`), then runs `culibox init`. When the release tarball holds `abduco` (from `v0.36.0`), it installs that next to `culibox`, where CuliBox looks first; for an older release it leaves abduco to you. With `--from-source` it builds the pinned abduco when none is installed and `cc` and `make` are present, and otherwise says how to install it. `init` is a guided setup that checks the programs CuliBox needs (offering `brew install` on macOS, one package at a time, and only printing commands on Linux), your workspace folder, the macOS box VM size, a Claude token, the config file, `box setup` and `culibox doctor`. Each of its 8 steps prints one result line, and you can run it again at any time to continue. Every step of both, so you can do it by hand, is in [docs/MACOS.md](MACOS.md#install). Options: `--version vX.Y.Z`, `--from-source` (build that release instead of downloading a binary) and `--prefix DIR`.

`setup.sh` refuses a release older than `v0.35.0`, the first one that includes `culibox init`. To build from source instead, follow [From source (manual)](#from-source-manual).

Then start CuliBox:

```sh
culibox serve
```

CuliBox listens on `127.0.0.1:8899` and prints a login URL with a login token. Open it in a browser on the same machine. The token is generated on the first start and kept in `login.token` in the data root; it stays valid across restarts until you delete that file (which makes a new one).

## Install from a release tarball

Each release on the [Releases page](https://github.com/culiops/culibox/releases) has a tarball for Linux and for macOS, each for amd64 and arm64, and a `SHA256SUMS` file. A tarball holds `culibox`, `abduco`, `LICENSE`, `LICENSE.abduco` (abduco's licence) and `README.md`; tarballs before `v0.36.0` hold no `abduco`. It does not include Docker; see [Requirements](#requirements). Keep `abduco` in the same folder as `culibox`: CuliBox uses it from there when neither the file nor its folder can be changed by another account (see `abduco_bin` in [docs/CONFIG.md](CONFIG.md)). To build from source instead, use [From source (manual)](#from-source-manual). The [setup.sh installer](#install-with-setupsh) does the download and the check below for you.

On Linux (use `arm64` on an ARM machine, and the release you want in place of `0.36.0`):

```sh
VERSION=0.36.0
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

**macOS binaries are unsigned.** A file downloaded with `curl` is not quarantined and runs as it is. A file downloaded in a browser is quarantined, and the flag can carry over to the files you extract from it. Remove it from both programs before the first run, since culibox runs `abduco` too (for a file without the flag, `xattr` prints a `No such xattr` error and still clears the other):

```sh
xattr -d com.apple.quarantine culibox abduco
```

**Is the macOS build verified?** Every release's notes end with a Platform status block. The Linux lines say the build was tested in CI, with a link to the run. The macOS lines say one of these:

- `⚠️ NOT yet verified on macOS`: nobody has checked these files on a Mac yet.
- `✅ verified on macOS ...`: the exact published files were checked on a Mac. The line gives the macOS version, the chip and the date.
- `⚠️ automated checks passed, manual checks not run`, or `⚠️ automated checks incomplete`: only part of the check ran.
- `❌ failed: ...`: a check failed. Do not use that build.

Releases are checked on a Mac in batches, so a new release usually starts as not verified. The block's last line, "Last macOS-verified release", names the newest verified release at the time this release was cut. It is not updated afterwards, so check the Releases page to see whether a newer release has been verified since. If you want a verified macOS build, pick the newest release marked ✅. The amd64 build is checked on an Apple Silicon Mac under Rosetta 2, not on Intel hardware. How the check works is in [docs/MACOS.md](MACOS.md#release-verification).

## From source (manual)

This is what `setup.sh --from-source` and `culibox init` do for you.

**1. Build.** Build into `~/.local/bin`, not into the source folder: a folder that holds a program CuliBox runs can only be shared with the box VM read-only.

```sh
git clone https://github.com/culiops/culibox.git
cd culibox
mkdir -p ~/.local/bin
go build -ldflags "-X main.version=$(scripts/version.sh)" -o ~/.local/bin/culibox ./cmd/culibox
```

Then install abduco, which the daemon needs to start. Your package manager's `abduco` works where there is one (`brew install abduco` on macOS). Or build the version CuliBox pins, from its checksum-verified source, next to `culibox` (it needs `curl`, `tar`, `cc` and `make`):

```sh
scripts/ci/install-abduco.sh ~/.local
culibox doctor
```

That leaves `~/.local/bin/abduco`, in the folder CuliBox looks in first, and `doctor` names the abduco it found.

Add `~/.local/bin` to your `PATH` if it is not there. Releases are `vX.Y.Z` git tags. `scripts/version.sh` prints the checkout's version (`v0.1.0`, or `v0.1.0-3-g<sha>` for 3 commits past the tag). The header shows it, and the About tab and `culibox bug-report` name it. A build without the flag works but shows no version.

**2. Try it uncontained.** With no config file, CuliBox runs one `claude` agent on the **host** tier (not contained):

```sh
culibox serve
```

It prints a login URL with a login token. Open it in a browser on the same machine. The token is kept in `login.token` in the data root and stays valid until you delete the file.

**3. Turn on boxes.** Get the box image (on macOS this also creates the VM; see [docs/MACOS.md](MACOS.md)):

```sh
culibox box setup
```

A release binary (from the Releases page or `setup.sh`) carries the digest of the published box image, `ghcr.io/culiops/culibox@sha256:...`, built from the same Dockerfile as the binary. With no `box_image` in your config, `box setup` (and `culibox init`, which runs it) pulls that digest for your Docker daemon's architecture (linux/amd64 or linux/arm64; on macOS, into CuliBox's box VM) and tags it `culiops/culibox:base`. On a Mac, one measured pull took about 39 s against 360 s for a local build (#238). Nothing is pulled when `culiops/culibox:base` is already there, native and built from this binary's Dockerfile, and an image already in the store is only re-tagged, which works offline. After a pull, `box setup` prints `pin this instance: set "box_image": "<ref>" in its config`: pinning keeps that exact image across upgrades.

If the pull fails (offline, registry down, egress blocked, or a TLS-intercepting proxy the Docker daemon does not trust), or the pulled image does not fit (another architecture or Dockerfile), `box setup` prints `WARN image: could not use the published image <ref>: <reason>; building it here instead (this takes several minutes)` and builds locally. A build from source (`go build`, without the release flags) carries no digest and always builds locally. `culibox box setup -rebuild` builds the image locally even when it is current, and never pulls. To always build on one instance, set `"box_image": "culiops/culibox:base"`: an explicit `box_image` is never replaced by the published image, and any explicit value (a dated tag, a digest, a registry ref) behaves as it did before.

`box setup` tags every local build with its date (for example `culiops/culibox:2026-10-18`) and prints how to pin it. To pull a particular published image instead, set `"box_image": "ghcr.io/culiops/culibox@sha256:<digest>"` (the digest is in the CI job summary, or from `docker buildx imagetools inspect ghcr.io/culiops/culibox:base`) and run `box setup`: it pulls the image for your daemon's architecture and never builds it. Pin by digest, never by `:base`.

After a successful `box setup`, it removes the old box images that are safe to lose, since each image is about 4 GB and an upgrade used to leave the previous one behind for good. By default that is only (a) an image a registry can supply again (named only under `ghcr.io/culiops/culibox` or the older `ghcr.io/culiops/agent-box`, with a registry digest: the worst case is a `box setup` pull), and (b) the one image this `box setup` orphaned, the one the configured name pointed at before the pull or build moved it (the `:base` upgrade leak). Exception to know about: (b) is usually a local build and still carries the dated tag its own build gave it, the one an earlier `box setup` told you to pin. It is removed all the same, and the output says so. If another CuliBox on this Docker is pinned to that tag from a config `box setup` cannot see (its own `CULIBOX_HOME` or `-config`) and has no box running, it loses the image, and needs `culibox box setup -rebuild` and its `box_image` updated to the new dated tag. `-no-prune` is the opt-out. Every other unused box image built here cannot be pulled again, and another CuliBox on this Docker may pin it (boxes are removed when idle, so "no container" is the normal state of another instance, and `box setup` cannot see a config kept at another path). So it is listed with its size and a `docker image rm` line (on macOS the line carries the box VM's `--host` and `--config`, quoted for the shell), and `culibox box setup -prune-local` removes ALL the listed images. `-no-prune` keeps everything. Always kept, in every mode: the image(s) this setup just made (also the new dated tag a `-rebuild` builds on a pinned instance), every image a container uses (running or stopped), every image a `box_image` pins in a config it can see (this instance's, the two default data roots', the `~/.config` ones), whatever is tagged `culiops/culibox:base` or the older `culiops/agent-box:base` (what an instance with no `box_image`, or a CuliBox from before the rename, runs), and an image that a saved upgrade backup's config pins as a dated tag or `:base` (a rollback that restores the backup needs it; it can only be built here). Images kept only for a default name or a backup are listed with their size and the command that frees them. A `box_image` in another config that is not a valid image reference is skipped with a warning. A box image is one that carries CuliBox's Dockerfile label and is named only under `culiops/culibox`, `culiops/agent-box` or their `ghcr.io` forms; anything else, such as an image you built from the box image, is never touched. The output names each removed image and the bytes freed (an upper bound: layers a kept image shares stay), and a failed removal is a warning, never a failed setup. Docker's build cache is not touched: it is shared with your other projects (`docker builder prune` clears it).

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

Open `http://127.0.0.1:8899` (use the printed login URL the first time). Sessions are boxed unless a profile says `"runtime": "host"`. The workspace folder must not be your home directory, because a box mounts the whole workspace. Claude Code needs a login inside the box: create a long-lived token with `claude setup-token` and paste it on an agent account's row in Settings → Agent accounts, then choose that account on the claude profile (Settings → Profiles), or add `"account": "<name>"` to the claude agent in `config.json` (see [Features](FEATURES.md); `culibox init` does all of this for an account named `claude-box`).

Every key is in [Configuration](CONFIG.md); [config.example.json](../config.example.json) is a fuller example. Only one CuliBox can serve a data root at a time.

## Run as a service

`culibox serve` runs in the foreground. To keep CuliBox running without a terminal, and start it again after a reboot, install it as a service of your own user (opt-in; nothing needs `sudo`):

    culibox service install     # write the unit and start it
    culibox service status      # installed? running? which binary? (exit 0 = running, 3 = not, 1 = could not tell)
    culibox service uninstall   # stop it and remove the unit

On Linux this is a `systemd --user` unit, `~/.config/systemd/user/culibox-serve.service` (under your user manager's `XDG_CONFIG_HOME` when it has one); on macOS a LaunchAgent, `~/Library/LaunchAgents/dev.culiops.culibox.plist` (see [MACOS.md](MACOS.md#run)). Pass the same `-config <path>` you give `culibox serve`, if any.

- **What it runs.** The `culibox` you ran `install` with, as `culibox serve -service` (plus `-config <absolute path>` when you gave one), with the `PATH`, locale (`LANG`, `LC_ALL`, `LC_CTYPE`), `CULIBOX_HOME` and an absolute `XDG_CONFIG_HOME` of the shell you ran it from (the last two written empty when unset, so the service resolves the same config as `install` did), so the service finds docker, git and claude where your shell does. Nothing else from your environment, and never a secret.
- **Loopback only, at every start.** With `-service`, serve refuses to start when `bind_addr` is not loopback or `password_env` is set, and neither systemd nor launchd restarts it after that refusal. So editing `bind_addr` to a network address later stops the service at its next start instead of exposing it; the reason is in the log.
- **When install refuses.** systemd is older than 240 or `systemctl --user` cannot reach your user manager; `bind_addr` is not loopback (`127.0.0.1`, `::1`, `localhost`); `password_env` is set (the service has no way to get the password, so use the login token); a CuliBox is already serving that data root or port (stop it; its sessions survive); the data root is still the old `~/.local/share/agent-box` (run `culibox serve` once so it moves the root); `data_dir` is a relative path; the binary is a temporary `go run` build; or a file it did not write is already at the unit's path. It warns about each `static_tokens` entry that reads an env var, because the service will not have it; keep those values in the [store](STORE.md) instead.
- **Logs** go to `service.log` in the data root, which also holds the login URL printed at start; `install` creates it readable by you only (`0600`). `install` waits two seconds and fails, pointing at the log, when the service did not stay up. Open `http://127.0.0.1:8899/login` (your `bind_addr`) and paste the token from `login.token` in the data root.
- **Sessions survive** a stop, a restart and `uninstall`; the next start reattaches them. On Linux they do not survive logging out unless lingering is on (next point).
- **After you replace the binary or change `PATH`**, run `culibox service install` again: it rewrites the unit and restarts the service. `status` and `doctor` say when the unit runs a different binary.
- **Linux: logging out.** A `systemd --user` service stops when your last login session ends, and starts at your next login. Its running agent sessions are killed then too, because they run under your user manager, which stops with it (a `culibox serve` you started yourself in a terminal does not have this problem with systemd's default `KillUserProcesses=no`). To keep the service and its sessions running after logout, and start it at boot, enable lingering once: `loginctl enable-linger $USER` (it may ask for an admin password). `install`, `status` and `doctor` say whether it is on. CuliBox never runs it for you.
- **Uninstall** removes only the unit file, and only if CuliBox wrote it (it looks for a marker line and never follows a symlink). It keeps `service.log` and everything else in the data root.
- `culibox doctor` has a `service` row: not installed, installed and running, or what is wrong.

## Upgrading

Upgrading an existing install, and going back: [UPGRADING.md](UPGRADING.md).
