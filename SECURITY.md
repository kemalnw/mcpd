# Security model

`mcpd` is intentionally a remote operating-system capability server. MCP tools
execute with the same operating-system permissions as the user running the
daemon. There is no command sandbox or path sandbox in the core execution
model.

Running `mcpd` as an unprivileged user gives the connected AI that user's
permissions. Running it as `root` gives the connected AI root permissions.
This is deliberate and part of the project's execution model.

Remote deployments can enable the embedded OAuth 2.1 authorization/resource
server. OAuth uses Authorization Code + PKCE S256, CIMD client metadata, an
Argon2id owner credential, Ed25519-signed access tokens, explicit MCP resource
audiences, and per-tool `mcp:read` / `mcp:write` scopes.

The owner password policy enforces a minimum of 8 characters; a longer passphrase
is recommended. Passwords are stored only as salted Argon2id verifiers and are
read without terminal echo during interactive setup.

`mcpd` is an HTTP-only origin server and defaults to `127.0.0.1:31354`. Do not
expose that plain-HTTP origin directly to an untrusted network. Remote production
deployments should enable `auth.enabled = true` and terminate HTTPS in a
user-managed reverse proxy/load balancer before forwarding to mcpd.

The systemd service does not require privileged ports or `CAP_NET_BIND_SERVICE`.
The installer defaults to the invoking `SUDO_USER`; root service mode remains
explicit via `--user root`. OAuth signing material and the owner password verifier
are stored beneath the configured state directories with restrictive permissions.

OAuth client metadata is attacker-controlled input. `mcpd` therefore fetches
CIMD only over HTTPS, blocks private/loopback/link-local resolved addresses,
disables redirects, caps metadata size, and validates exact `client_id` and
redirect URI binding.

Official release artifacts are published by the tag-driven GitHub Actions release
workflow. Archives and the installer are covered by SHA-256 checksums, keyless
Sigstore/Cosign bundles bound to the exact workflow/tag identity, and GitHub build
provenance attestations. The installer always checks SHA-256 and verifies the
Sigstore checksum-manifest signature automatically when Cosign is available; set
`MCPD_REQUIRE_SIGNATURE=1` to make signature verification mandatory.

Please report vulnerabilities privately through GitHub's security reporting
feature rather than opening a public issue.
