# Security model

`mcpd` is intentionally a remote operating-system capability server. MCP tools
execute with the same operating-system permissions as the user running the
daemon. There is no command sandbox or path sandbox in the core execution
model.

Running `mcpd` as an unprivileged user gives the connected AI that user's
permissions. Running it as `root` gives the connected AI root permissions.
This is deliberate and part of the project's execution model.

Authentication and transport security are tracked as first-class capabilities
and will be implemented before a production-ready release. The current
bootstrap branch is for local/integration development and exposes no auth yet.

Please report vulnerabilities privately through GitHub's security reporting
feature rather than opening a public issue.
