# Migrating from v0.1.x to v0.2.0

`v0.2.0` removes built-in TLS/ACME and systemd socket activation. `mcpd` becomes
an HTTP-only MCP/OAuth origin server. HTTPS is provided by the deployment owner.

Existing `[tls]` configuration is rejected rather than silently ignored.
The old `8787` development/backend default is replaced by `127.0.0.1:31354`.

## Before upgrading

Provide a public HTTPS frontend for your MCP domain and forward it to the new
default backend:

```text
ChatGPT
  -> HTTPS mcp.example.com:443
  -> user-managed TLS frontend
  -> HTTP 127.0.0.1:31354
  -> mcpd
```

With Cloudflare **DNS only**, the A/AAAA record points directly to the VM. The VM
must therefore have the user-managed HTTPS frontend on port 443; DNS alone does
not terminate TLS or translate port 443 to port 31354.

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

Install the new binary and run the v0.2 setup wizard:

```bash
sudo mcpd setup
```

Choose **reconfigure** when the existing v0.1 config contains `[tls]`. The setup
flow disables/removes the legacy `mcpd.socket` unit and installs only
`mcpd.service`. OAuth owner/signing state under `/var/lib/mcpd/auth` is preserved
when the same state path is retained.
