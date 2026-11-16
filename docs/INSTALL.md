# Install CuliBox

The short version is the [Quick start in the README](../README.md#quick-start). This page has the details: what CuliBox needs, what the installer does, installing a release tarball by hand, building from source, and how to tell whether a macOS build is verified.

## Requirements

- Go 1.26.5 or newer (see `go.mod`) to build.
- Linux or macOS. macOS needs 13+ on Apple Silicon (Intel is untested) for the boxed tier.
- [abduco](https://www.brain-dump.org/projects/abduco/), in the same folder as the `culibox` binary or on `PATH` (CuliBox looks in that order; see `abduco_bin` in [docs/CONFIG.md](CONFIG.md)). It is required; the daemon will not start without it.
- For the boxed tier: Docker on Linux, or `colima` and `docker` on macOS. On Linux the docker daemon must run on the same machine and see host files as they are (a local Docker Engine). A VM-backed or remote daemon (for example Docker Desktop for Linux or a remote docker context) is not supported for boxed sessions: CuliBox refuses to start a box there, because it cannot confirm the box got the folder that was checked.
- The agent CLI you want to run. Claude Code (`claude`) is the supported agent. Other terminal programs can be configured as agents (a shell, for example), but status, usage and reply features are Claude Code only.
- Node.js, only to run the browser-side tests in development.

## Install with setup.sh

Download the installer, read it, run it (use the release tag you want in place of `<tag>`):

```sh
curl -fsSLO https://raw.githubusercontent.com/culiops/culibox/<tag>/scripts/setup.sh
sh setup.sh
```

`setup.sh` is a short POSIX `sh` script. It installs a checksum-verified `culibox` into `~/.local/bin` (it never uses `sudo` and installs only under that folder; it downloads and builds in a temporary folder that it removes, and with `--from-source` on macOS it may run `brew install go` after an explicit `y`), then runs `culibox init`. When the release tarball holds `abduco`, it installs that next to `culibox`; with `--from-source` it builds the pinned abduco when none is installed and `cc` and `make` are present, and otherwise says how to install it. `init` is a guided setup that checks the programs CuliBox needs (offering `brew install` on macOS, one package at a time, and only printing commands on Linux), your workspace folder, the macOS box VM size, a Claude token, the config file, `box setup` and `culibox doctor`. Each of its 8 steps prints one result line, and you can run it again at any time to continue. Every step of both, so you can do it by hand, is in [docs/MACOS.md](MACOS.md#install). Options: `--version vX.Y.Z`, `--from-source` (build that release instead of downloading a binary) and `--prefix DIR`.

`setup.sh` refuses a release older than `v0.35.0`, the first one that includes `culibox init`. To build from source instead, follow [From source (manual)](#from-source-manual).

Then start CuliBox:

```sh
culibox serve
```

CuliBox listens on `127.0.0.1:8899` and prints a login URL with a login token. Open it in a browser on the same machine. The token is generated on the first start and kept in `login.token` in the data root; it stays valid across restarts until you delete that file (which makes a new one).

## Install from a release tarball

Each release on the [Releases page](https://github.com/culiops/culibox/releases) has a tarball for Linux and for macOS, each for amd64 and arm64, and a `SHA256SUMS` file. A tarball holds `culibox`, `LICENSE` and `README.md`. It does not include abduco or Docker; see [Requirements](#requirements). To build from source instead, use [From source (manual)](#from-source-manual). The [setup.sh installer](#install-with-setupsh) does the download and the check below for you.

On Linux (use `arm64` on an ARM machine, and the release you want in place of `0.35.0`):

```sh
VERSION=0.35.0
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

Releases are checked on a Mac in batches, so a new release usually starts as not verified. The block's last line, "Last macOS-verified release", names the newest verified release at the time this release was cut. It is not updated afterwards, so check the Releases page to see whether a newer release has been verified since. If you want a verified macOS build, pick the newest release marked ✅. The amd64 build is checked on an Apple Silicon Mac under Rosetta 2, not on Intel hardware. How the check works is in [docs/MACOS.md](MACOS.md#release-verification).

## From source (manual)

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

**3. Turn on boxes.** Build the box image (on macOS this also creates the VM; see [docs/MACOS.md](MACOS.md)):

```sh
culibox box setup
```

`box setup` tags every build with its date (for example `culiops/culibox:2026-10-18`) and prints how to pin it. To pull instead of build, set `"box_image": "ghcr.io/culiops/culibox@sha256:<digest>"` (the digest is in the CI job summary, or from `docker buildx imagetools inspect ghcr.io/culiops/culibox:base`) and run `box setup`: it pulls the image for your daemon's architecture instead of building it. Pin by digest, never by `:base`.

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

## Upgrading

Upgrading an existing install, and going back: [UPGRADING.md](UPGRADING.md).
