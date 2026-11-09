# Remote access and notifications

CuliBox listens on loopback (`127.0.0.1:8899`) by default and has no TLS of its own. This page is for reaching it from another machine and still getting desktop notifications.

## Why the address matters

Browsers only offer some features, including operating-system notifications, to a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts): a page served over HTTPS, or from `localhost` / `127.0.0.1`. The [Notification API](https://developer.mozilla.org/en-US/docs/Web/API/Notification) is one of them.

So `http://127.0.0.1:8899` on the machine that runs CuliBox gets notifications, and `https://...` does too. A plain `http://` address on a LAN or tailnet IP does not. There CuliBox falls back to what works anywhere: the attention chip, the optional chime and the tab title. Settings > Alerts says which case you are in.

CuliBox does not change how it is served for this. It has no built-in TLS, and you should not bind it to `0.0.0.0` on a public IP to make notifications work. Keep the bind on loopback or a private network and read [Security model](SECURITY.md) and the `allow_public` row in [Configuration](CONFIG.md) first. Both options below leave the bind on loopback.

## Option A: HTTPS on a tailnet name

[Tailscale Serve](https://tailscale.com/kb/1242/tailscale-serve) terminates HTTPS on the machine that runs CuliBox and proxies to the loopback port. You need HTTPS enabled for your tailnet (admin console, DNS page; see [Enabling HTTPS](https://tailscale.com/kb/1153/enabling-https)).

1. On the machine that runs CuliBox:

   ```sh
   tailscale serve --bg --https=443 http://127.0.0.1:8899
   ```

   The address is then `https://<machine>.<tailnet>.ts.net`. The proxy is expected to forward the browser's `Host` header (the tailnet name) rather than rewrite it; if you see "host not allowed", check the logged `Host` in step 2. To stop serving, repeat the command with `off` at the end.

2. Tell CuliBox to accept that name. CuliBox refuses any request whose `Host` is not on its allowlist (a DNS-rebinding defence), so add the name, without scheme or path, to `allowed_hosts` in `config.json`:

   ```json
   {
     "allowed_hosts": ["<machine>.<tailnet>.ts.net"]
   }
   ```

   Restart CuliBox. A refused request is answered with "host not allowed" and the refused `Host` is logged, so you can check the exact value to add. See [Configuration](CONFIG.md).

3. Open `https://<machine>.<tailnet>.ts.net` and allow notifications when the browser asks.

Before you enable it: per Tailscale's HTTPS docs, every certificate is recorded in public Certificate Transparency logs, including the full name of each machine that gets one. Do not use this if the machine name is sensitive.

Any reverse proxy you run yourself that serves HTTPS to the loopback port works the same way: add its public name to `allowed_hosts`.

## Option B: SSH port forward

No certificate, no config change, and nothing exposed. On the machine you browse from:

```sh
ssh -L 8899:127.0.0.1:8899 <user>@<host>
```

Leave it running and open `http://127.0.0.1:8899`. That is a localhost origin, so it is a secure context and notifications work. The `Host` is `127.0.0.1:8899`, which CuliBox already accepts. That holds only when the local port equals the port CuliBox binds to: CuliBox accepts the loopback names (`127.0.0.1`, `localhost`, `::1`) only on its own bind port.

If local port 8899 is taken, pick another, for example `-L 9000:127.0.0.1:8899`, and open `http://127.0.0.1:9000`. That `Host` (`127.0.0.1:9000`) is not accepted and is answered with "host not allowed", so add it to `allowed_hosts` first:

```json
{
  "allowed_hosts": ["127.0.0.1:9000"]
}
```

Restart CuliBox. Notifications are tied to the origin, so allow them again for it.
