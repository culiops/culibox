# Contributing

CuliBox takes **issues, not pull requests.**

- **Bug reports and proposals go in [issues](https://github.com/culiops/culibox/issues).**
- **Pull requests from outside are closed with thanks.** Changes to CuliBox are made through CuliBox's own reviewed agent sessions, so a patch from outside has nowhere to land. This is not a comment on your patch. A clear issue is the most useful thing you can send.

## Reporting a bug

A good report lets someone reproduce the problem without asking you questions.

1. **Title: say in plain words what is wrong**, for example "box refuses to start after upgrading to v0.35.0", not "doesn't work".
2. **Paste the bug report bundle.** Run `culibox bug-report` (on macOS, `culibox bug-report | pbcopy`), or use Settings, About, Report a problem in the web UI. It includes the version (`culibox version` prints it), your OS and architecture, the output of `culibox doctor`, a summary of your settings, and runtime state. The bundle is redacted: it replaces your home folder and hostname, scrubs strings that look like tokens, and lists only an allowlist of settings with no paths, hosts or secrets. Read it before you post it anyway.
3. **Say what you did, what you expected and what happened instead.** Add steps to reproduce if you know them.

## Proposing a feature

Open an issue that says what you want to be able to do and why. Start from the problem rather than the solution. CuliBox is a control plane for running, watching and containing agent sessions; a proposal is easier to accept when it fits that.

## Security problems

**Never report a vulnerability in a public issue.** Report it privately, as described in [`.github/SECURITY.md`](.github/SECURITY.md).

## Licence

CuliBox is licensed under Apache-2.0. See [LICENSE](LICENSE).
