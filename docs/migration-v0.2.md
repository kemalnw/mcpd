# Migrating from v0.1.x to v0.2.0

`v0.2.0` removes built-in TLS/ACME and systemd socket activation. `mcpd` remains
an HTTP-only MCP/OAuth origin server. Current `mcpd setup` can manage Caddy as the
standard HTTPS frontend without putting TLS back into the Go daemon.

Existing `[tls]` configuration is rejected rather than silently ignored.
The old `8787` development/backend default is replaced by `127.0.0.1:31354`.

## Before upgrading

Ensure the MCP domain resolves to the VM. The default setup path can provision
Caddy and forward public HTTPS to the backend:

```text
ChatGPT
  -> HTTPS mcp.example.com:443
  -> Caddy (managed by mcpd setup) or existing TLS frontend
  -> HTTP 127.0.0.1:31354
  -> mcpd
```

With Cloudflare **DNS only**, the A/AAAA record points directly to the VM. Managed
Caddy can own ports 80/443 and automatic certificates; DNS alone still does not
terminate TLS or translate port 443 to port 31354. Existing reverse proxies or
Cloudflare Tunnel remain supported by selecting the external-frontend mode.

## Configuration changes

Change the server and auth sections to this shape:

```toml
[server]
listen = "127.0.0.1:31354"
mcp_path = "/mcp"

[auth]
enabled = true
external_url = "https://mcp.example.com"
```

Remove the entire `[tls]` section. Keep `auth.external_url` as the canonical
public HTTPS origin even though the local `mcpd` listener is HTTP.

## systemd migration

Install the new binary and run the setup wizard:

```bash
sudo mcpd setup
```

Choose **reconfigure** when the existing v0.1 config contains `[tls]`, then select
**Caddy (recommended, automatic HTTPS)** unless an existing reverse proxy/tunnel
already owns HTTPS. The setup flow disables/removes the legacy `mcpd.socket` unit
and keeps `mcpd.service` as the only mcpd-owned systemd unit; managed Caddy uses its
packaged `caddy.service`.

OAuth owner/signing state under `/var/lib/mcpd/auth` is preserved when the same
state path is retained.
