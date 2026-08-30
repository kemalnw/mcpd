# Releases and verification

Official releases are published from Git tags matching `v<semver>` by
`.github/workflows/release.yml`. The release workflow only accepts tags whose
commit is already reachable from `main`.

Each release currently contains:

- `mcpd_<version>_linux_amd64.tar.gz`
- `mcpd_<version>_linux_arm64.tar.gz`
- `install.sh`
- `checksums.txt`
- one `.sigstore.json` bundle for every tarball, `install.sh`, and `checksums.txt`
- a GitHub build-provenance attestation for the files listed in `checksums.txt`

Build metadata (`version`, commit SHA, and commit timestamp) is injected through
Go linker flags. Archives use a fixed source timestamp, numeric owner/group 0,
sorted entries, and `gzip -n` so the same source inputs produce identical
release archives.

## Install

The stable one-line binary installer is the `install.sh` asset from the latest
GitHub Release:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

It detects Linux `amd64`/`arm64`, downloads the matching release tarball and
`checksums.txt`, verifies SHA-256, rejects unexpected archive paths, and installs
the binary to `/usr/local/bin` (using `sudo` only for the final filesystem write
when required).

The script does not silently configure a system service. After installing the
binary, run:

```bash
sudo mcpd install
```

To install the systemd service in the same flow, use:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | MCPD_INSTALL_SERVICE=1 sh
```

## Verify with Sigstore

If `cosign` is already installed, `install.sh` automatically verifies the
signature on `checksums.txt`. To require that stronger verification instead of
allowing checksum-only installation, set:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | MCPD_REQUIRE_SIGNATURE=1 sh
```

For manual verification, download an artifact and its bundle, then use the exact
workflow identity for the release tag:

```bash
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/kemalnw/mcpd/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
```

GitHub provenance can additionally be verified with `gh attestation verify`.

## Maintainer release flow

Releases are tag-driven. After the intended commit is merged to `main`:

```bash
git checkout main
git pull --ff-only
git tag vX.Y.Z
git push origin vX.Y.Z
```

The workflow validates the tag, reruns source checks, builds both architectures,
signs every release asset keylessly through GitHub OIDC/Sigstore, creates build
provenance, verifies the generated signatures, and only then creates the GitHub
Release.

After deploying an MCPD upgrade, verify the public endpoint with `scripts/verify-live-schema.sh`. `/healthz` publishes both the binary version and a tool-catalog version/fingerprint. The verifier can compare them using `MCPD_EXPECT_VERSION`, `MCPD_EXPECT_CATALOG_VERSION`, and `MCPD_EXPECT_CATALOG_FINGERPRINT`. If the live server reports the expected catalog but a connected client still lacks a tool or input field, the remaining problem is client-side schema caching; reconnect/reload that MCP client rather than repeatedly restarting the daemon. Tags containing a prerelease suffix such as `v1.0.0-rc.1` produce a
GitHub prerelease.

Do not manually replace assets on an existing version. Publish a new version so
checksums, signatures, provenance, tag, and release notes continue to describe a
single immutable build.

## Verify schema-changing upgrades

A restarted service and a correct binary version do **not** prove that an already-connected MCP client has consumed a new tool schema. After deploying a release that changes tool inputs/outputs, verify both layers separately:

```bash
/usr/local/bin/mcpd version
./scripts/verify-live-schema.sh https://mcp.example.com start_search pathHint
```

`verify-live-schema.sh` performs a fresh HTTP `GET /healthz` and a fresh MCP `tools/list`, then checks that the expected tool input field is present. Set `MCPD_EXPECT_VERSION=vX.Y.Z` when the live binary version must also match exactly, and `MCPD_MCP_PATH=/custom-path` for a non-default MCP endpoint.

If the fresh live check passes but ChatGPT or another already-connected client still exposes the old schema, reconnect/reload that MCP connection so the client performs a new `tools/list`. Do not restart the daemon repeatedly: the remaining problem is client-side metadata caching, not the running server. This distinction should be part of every schema-changing release verification.
