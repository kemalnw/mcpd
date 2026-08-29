# Setup

`mcpd setup` is the interactive first-run and reconfiguration path for a remote
MCP deployment. `mcpd` itself stays an HTTP-only origin server; the default setup
path manages Caddy as the public HTTPS/TLS frontend.

## Interactive install

On a supported Linux VM:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

When a controlling terminal is available, the verified release installer invokes
`sudo mcpd setup` automatically. The wizard asks for the public domain, service
user, OAuth owner password, backend listener, MCP path, and HTTPS frontend. Caddy
(automatic HTTPS) is the default; an existing reverse proxy or tunnel is optional.

The normal defaults are:

```text
backend        http://127.0.0.1:31354
MCP path       /mcp
OAuth          enabled
HTTPS frontend Caddy
```
After confirmation, setup writes `/etc/mcpd/config.toml`, installs/enables
`mcpd.service`, configures the owner credential, starts the daemon, installs and
configures Caddy when selected, verifies local and public health/OAuth endpoints,
and runs `mcpd doctor`.

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

Without a TTY, `mcpd setup` never prompts. Managed Caddy is the default:

```bash
printf '%s\n' "$MCPD_OWNER_PASSWORD" | sudo mcpd setup \
  --domain mcp.example.com \
  --yes \
  --password-stdin
```

Pass `--https-ready` only when an existing reverse proxy/tunnel already owns public
HTTPS. Use `--reconfigure` before replacing an existing config. Use `--no-auth` only
for a deliberately unauthenticated deployment. `mcpd install` remains available as
a lower-level deterministic primitive for automation that already has a config file.

The release installer itself never reads setup answers from a pipe. In non-TTY
contexts it installs the binary and exits with an instruction to run
`sudo mcpd setup`. `MCPD_SETUP=0` disables automatic setup explicitly.

## HTTPS boundary

A DNS record maps a name to an address; it does not create a certificate or map
public port 443 to the backend. The default direct-VM deployment is:

```text
ChatGPT
  -> HTTPS mcp.example.com:443
  -> Caddy
  -> HTTP 127.0.0.1:31354
  -> mcpd
```

When Caddy is selected, setup:

- requires the public hostname to resolve;
- checks local `:80` and `:443` availability before installing a new frontend;
- installs Caddy through `apt-get`, `dnf`, or `yum` when it is not already present;
- manages `/etc/caddy/mcpd.caddy` and one idempotent import in the packaged Caddyfile;
- validates the full Caddy config before activation;
- enables/starts or reloads `caddy.service`;
- waits for public `/healthz` and OAuth discovery over verified HTTPS.

The VM/cloud firewall must allow public TCP `80` and `443` for the normal managed
Caddy path. Port `31354` remains loopback-only and does not need a public rule.

With Cloudflare **DNS only**, the public domain resolves directly to the VM and
Caddy terminates TLS. With Cloudflare **Proxied** or Cloudflare Tunnel, select the
existing reverse proxy/tunnel mode; Cloudflare's origin/TLS policy remains outside
`mcpd`.

Existing nginx, HAProxy, or custom Caddy deployments are also supported through
that external-frontend mode and are not mutated by `mcpd setup`.

## ChatGPT

After public HTTPS is working, configure the ChatGPT plugin/server URL as:

```text
https://mcp.example.com/mcp
```

Choose OAuth authentication. `mcpd` publishes OAuth protected-resource and
authorization-server metadata from the configured canonical public origin, even
though Caddy or another frontend talks to the daemon over local HTTP.

For persistent authorization, `mcpd` advertises `offline_access` and rotating
refresh-token support. Access tokens still expire after 1 hour by default, but a
client holding a valid refresh token can renew them without asking for the owner
password again. The refresh authorization uses a 30-day sliding idle timeout by
default. Existing ChatGPT apps created before refresh-token support should be
disconnected/reconnected once after upgrade so ChatGPT fetches the updated OAuth
metadata and obtains its first refresh token.

If ChatGPT cannot connect, run `mcpd doctor`. It reports the local backend and
public layers separately (`backend-health`, `public-dns`, `public-https`, and
`public-oauth`, plus Caddy service/config checks when managed Caddy is detected):

```bash
mcpd doctor
mcpd logs --lines 100
curl http://127.0.0.1:31354/healthz
```
