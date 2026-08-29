# Setup

`mcpd setup` is the interactive first-run and reconfiguration path for a remote
MCP deployment. `mcpd` itself is an HTTP-only origin server; the deployment owner
provides public HTTPS separately.

## Interactive install

On a supported Linux VM:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

When a controlling terminal is available, the verified release installer invokes
`sudo mcpd setup` automatically. The wizard asks for the public domain, service
user, OAuth owner password, backend listener, MCP path, and whether external HTTPS
has already been configured.

The normal defaults are:

```text
backend  http://127.0.0.1:31354
MCP path /mcp
OAuth    enabled
```
After confirmation, setup writes `/etc/mcpd/config.toml`, installs/enables the
single `mcpd.service`, configures the owner credential, starts the daemon, runs
`mcpd doctor`, and checks the local health endpoint.

Owner passwords must contain at least 8 characters. A longer passphrase is
recommended. Interactive password input is never echoed.

## Rerunning setup

Run:

```bash
sudo mcpd setup
```

When an existing config is detected, the default choice is to keep it and repair
or reinstall the service. Reconfiguration requires an explicit choice. Existing
compatible OAuth state is preserved; setup does not silently replace secrets.

A v0.1.x config containing `[tls]` cannot be kept unchanged because TLS support
was removed in v0.2. Choose reconfigure and retain the same public domain and
OAuth state directory as appropriate.

## Non-interactive automation

Without a TTY, `mcpd setup` never prompts. Pass `--yes` and explicit values:
```bash
printf '%s\n' "$MCPD_OWNER_PASSWORD" | sudo mcpd setup \
  --domain mcp.example.com \
  --https-ready \
  --yes \
  --password-stdin
```

Use `--reconfigure` before replacing an existing config. Use `--no-auth` only for
a deliberately unauthenticated deployment. `mcpd install` remains available as a
lower-level deterministic primitive for automation that already has a config file.

The release installer itself never reads setup answers from a pipe. In non-TTY
contexts it installs the binary and exits with an instruction to run
`sudo mcpd setup`. `MCPD_SETUP=0` disables automatic setup explicitly.

## HTTPS boundary

A DNS record maps a name to an address; it does not map public port 443 to the
backend port or create a certificate. A typical deployment is:

```text
ChatGPT
  -> HTTPS mcp.example.com:443
  -> user-managed HTTPS frontend
  -> HTTP 127.0.0.1:31354
  -> mcpd
```
With Cloudflare **DNS only**, the public domain resolves directly to the VM, so
the VM must run the user-managed HTTPS frontend on `:443`. That frontend forwards
to `127.0.0.1:31354`. Cloudflare DNS-only mode does not terminate TLS.

With Cloudflare **Proxied**, Cloudflare participates in the HTTP/TLS path; its
origin policy is configured outside `mcpd`.

## ChatGPT

After public HTTPS is working, configure the ChatGPT plugin/server URL as:

```text
https://mcp.example.com/mcp
```

Choose OAuth authentication. `mcpd` publishes OAuth protected-resource and
authorization-server metadata from the configured canonical public origin, even
though the reverse proxy talks to the daemon over local HTTP.

For persistent authorization, `mcpd` advertises `offline_access` and rotating
refresh-token support. Access tokens still expire after 1 hour by default, but a
client holding a valid refresh token can renew them without asking for the owner
password again. The refresh authorization uses a 30-day sliding idle timeout by
default. Existing ChatGPT apps created before refresh-token support should be
disconnected/reconnected once after upgrade so ChatGPT fetches the updated OAuth
metadata and obtains its first refresh token.

If setup reports local success but ChatGPT cannot connect, verify the external
HTTPS frontend first, then run:

```bash
mcpd doctor
mcpd logs --lines 100
curl http://127.0.0.1:31354/healthz
```
