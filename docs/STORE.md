# Store

CuliBox can hold secret values itself: tokens, a GitHub App private key, the Claude OAuth token. Anywhere config takes a value reference (`file` or `env`) it can also say `{"type": "store", "id": "<id>"}`, and the older `secret_sources` list accepts `{"type": "store"}`. On disk there is only ciphertext (age), so the store directory is safe to back up.

## What it protects against, and what it does not

- **It protects against a disk snapshot or backup** taken by your hosting provider or a backup job: the values in the store are unreadable without the passphrase (Linux) or the login Keychain item (macOS).
- **It does not hide values from agents.** While the store is unlocked, any process running as your user, a host-tier agent included, can read the master key and so every value. Boxed agents cannot. A value still reaches the box the way it always did (env for a declared secret, `agentbox-env` for a static token, the git helper for GitHub).
- **It only protects what you move into it.** A value that is also in an env file, a shell profile or your home directory is still readable from a snapshot. The same goes for everything else on the machine that CuliBox does not own, such as SSH keys and cloud-CLI caches.
- It is not a vault product: one machine, one user, no sync.

**From the web UI.** **Settings → Credentials** is the usual way in. Its encrypted-store strip creates the store (**Create the encrypted store**) and, on Linux, locks it (**Lock now**). The forms that take a secret store it for you: **Add service token** and the GitHub App form put the pasted value or key in the store. **Stored values → Set a value** adds any other value by id. Values are write-only: you can replace or delete one, never read it back. The CLI below does the same without the browser.

**Commands.** The CLI honours `-config` like the rest of CuliBox, writes the files directly and does not need the daemon running.

| Command | What it does |
|---|---|
| `culibox store init` | Create the store. Linux asks for a passphrase (at least 12 characters; twice on a terminal, one line when piped) and leaves the store unlocked. macOS keeps the master key in the login Keychain. |
| `culibox store status` | Initialized or not, locked or unlocked, time left, number of values. |
| `culibox store unlock` | Linux: ask for the passphrase and unlock for `store.unlock_ttl`. |
| `culibox store lock` | Linux: lock now. |
| `culibox store passwd` | Linux: change the passphrase. Values are not re-encrypted. |
| `culibox store set <id>` | Read a value from stdin and store it. The value is never an argument. On a terminal it is read without echo; when piped, all of stdin is read and one trailing newline is trimmed. Works while locked. |
| `culibox store rm <id>` | Delete a value. |
| `culibox store list` | Ids, last-updated time and what refers to each id. There is no `get`, and values are never printed. |

An id is 1 to 64 characters of letters, digits, `_` and `-`, starting with a letter or digit, and a value is at most 64 KiB.

**Unlock and TTL.** On Linux the master key sits in the kernel user keyring, not in the daemon, so unlocking survives a daemon restart. It does not survive a reboot. Unlock with `culibox store unlock`, or from the web UI: a banner appears when the store is locked and Settings has a Lock button. Web unlock needs the browser session (an API bearer token cannot unlock) and failed attempts are rate limited. The key expires after `store.unlock_ttl` (default 7 days, a new unlock resets it, reads do not extend it); a lock or an expiry takes effect on the next value lookup. If you reach CuliBox through a tunnel or proxy that terminates TLS, the passphrase is visible to that proxy in transit when you type it into the web UI. `culibox store unlock` on the machine does not have that exposure. A forgotten passphrase cannot be recovered: remove `<data_dir>/store` and set the values again.

**After a reboot the store is locked.** Anything that needs a store value is then refused until someone unlocks: a spawn fails with `store is locked`, and a running boxed session's next token fetch gets a 503. If the Claude token is in the store, that means every Claude session. A host-tier agent hits this too.

**The Claude token.** The usual way is an agent account: **Settings → Agent accounts** takes a pasted token for a Claude account and stores it as `ACCT_<NAME>_TOKEN` (`culibox init` does this for the `claude-box` account).

**Moving a Claude token you keep in the daemon's environment.** If the token still comes from `CLAUDE_CODE_OAUTH_TOKEN` in the daemon's environment, you can move it into the store:

1. Create an agent account in **Settings → Agent accounts** and paste the token on its row (`printenv CLAUDE_CODE_OAUTH_TOKEN` shows it). The token goes into the store as `ACCT_<NAME>_TOKEN`.
2. Choose that account on your profiles. (A config that still uses the older `secret_sources` list can instead run `printenv CLAUDE_CODE_OAUTH_TOKEN | culibox store set CLAUDE_CODE_OAUTH_TOKEN` and add `{"type": "store"}` before the `env` source; see [Older forms that still load](CONFIG.md#older-forms-that-still-load).)
3. Delete the value from every env file and shell profile that still holds it (the file your service manager loads as `EnvironmentFile=`, your dev and prod copies, your login shell's profile).
4. Restart CuliBox and check for leftovers. At start the daemon logs one line for each name the store serves that is still in the daemon's own environment, and Settings shows the same list for the running daemon. `culibox doctor` only checks the environment it is run from, so it does not see what your service manager gives the daemon.

The protection exists only once every plaintext copy is gone.

**Docker's seccomp profile must stay on.** The box barrier for the master key is Docker's default seccomp profile, which refuses the `keyctl` calls a box would need to read it. On Linux, at each boxed spawn CuliBox checks that seccomp is active, and when it is not (and a store exists) it refuses the spawn, and `culibox doctor` fails. A box that was already running when you ran `culibox store init` is not re-checked: a container keeps the seccomp profile it was created with, so a box created while seccomp was off can read the keyring once the store is unlocked. If your host runs Docker with seccomp off, stop those boxes before `store init`. macOS boxes run in a VM and the store key stays in the host Keychain, so this gate does not apply there.

**On macOS** there is no lock cycle: the Keychain holds the master key, `status` shows `expires: never`, and `unlock`, `lock` and `passwd` do nothing.
