# Security model

`mcpd` is intentionally a remote operating-system capability server. MCP tools
execute with the same operating-system permissions as the user running the
daemon. There is no command sandbox or path sandbox in the core execution
model.

Running `mcpd` as an unprivileged user gives the connected AI that user's
permissions. Running it as `root` gives the connected AI root permissions.
This is deliberate and part of the project's execution model.

Remote deployments can enable the embedded OAuth 2.1 authorization/resource
server and HTTPS. OAuth uses Authorization Code + PKCE S256, CIMD client
metadata, an Argon2id owner credential, Ed25519-signed access tokens, explicit
MCP resource audiences, and per-tool `mcp:read` / `mcp:write` scopes.

Do not expose an unauthenticated `tls.mode = "off"` listener to an untrusted
network. Public deployments should enable `auth.enabled = true` and use either a
trusted certificate pair (`tls.mode = "files"`) or ACME (`tls.mode = "acme"`).
ACME private keys, OAuth signing material, and the owner password verifier are
stored beneath the configured state directories with restrictive permissions.

OAuth client metadata is attacker-controlled input. `mcpd` therefore fetches
CIMD only over HTTPS, blocks private/loopback/link-local resolved addresses,
disables redirects, caps metadata size, and validates exact `client_id` and
redirect URI binding.

Please report vulnerabilities privately through GitHub's security reporting
feature rather than opening a public issue.
