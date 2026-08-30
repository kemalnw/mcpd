# Releases

Official releases are created from tags matching `v<semver>` by
`.github/workflows/release.yml`. Release tags must point to commits already
reachable from `main`.

## Artifacts

A release includes Linux `amd64` and `arm64` archives, `install.sh`,
`checksums.txt`, Sigstore bundles, and GitHub build provenance.

The release workflow builds deterministic archives, signs release assets through
GitHub OIDC/Sigstore, verifies the generated signatures, and publishes the GitHub
Release only after the release checks pass.

## Install and verify

The normal installer is:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256 before installing. If `cosign` is available it also
verifies the Sigstore identity of `checksums.txt`.

Require signature verification explicitly with:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh \
  | MCPD_REQUIRE_SIGNATURE=1 sh
```

Set `MCPD_SETUP=0` when only the binary should be installed. Otherwise an
interactive terminal starts `mcpd setup`; non-interactive installs print the setup
command and exit without waiting for input.

For manual verification, use the release's `.sigstore.json` bundle with the exact
release-workflow identity for the tag. GitHub provenance can additionally be
verified with `gh attestation verify`.

## Maintainer flow

After the intended commit is on `main` and CI is green:

```bash
git checkout main
git pull --ff-only
git tag vX.Y.Z
git push origin vX.Y.Z
```

Do not replace assets on an existing release. Publish a new version so the tag,
checksums, signatures, provenance, and release notes continue to describe one
immutable build.

Prerelease tags such as `v1.0.0-rc.1` are published as GitHub prereleases.

## Verify a deployed upgrade

After deployment, verify the running binary and the live MCP tool catalog rather
than assuming a successful service restart means clients have refreshed their
schemas:

```bash
/usr/local/bin/mcpd version
./scripts/verify-live-schema.sh https://mcp.example.com start_search pathHint
```

`verify-live-schema.sh` performs fresh health and `tools/list` requests. Set
`MCPD_EXPECT_VERSION`, `MCPD_EXPECT_CATALOG_VERSION`, or
`MCPD_EXPECT_CATALOG_FINGERPRINT` when the deployment must match an exact release
or catalog.

If the fresh live check is correct but an already-connected client still exposes
an older schema, reconnect that MCP client. Repeated daemon restarts do not fix
client-side schema caching.

Before tagging, run the repository gates used by CI:

```bash
make check
make release-test
```
