# crush-acp-bridge

Experimenting with Crush.

## Installation

Pre-built binaries for Linux, macOS, and Windows (amd64 and arm64) are published to
[GitHub Releases](https://github.com/jamestelfer/crush-acp-bridge/releases). Every archive
carries a build provenance attestation.

Verify a downloaded archive with the [GitHub CLI](https://cli.github.com/):

```sh
gh attestation verify crush-acp-bridge_linux_amd64.tar.gz --repo jamestelfer/crush-acp-bridge \
  --signer-workflow chinmina/.github/.github/workflows/goreleaser-release.yml
```

Install with any of:

```sh
mise use -g github:jamestelfer/crush-acp-bridge
brew install jamestelfer/tap/crush-acp-bridge
nix profile install github:jamestelfer/nixpkgs#crush-acp-bridge
go install github.com/jamestelfer/crush-acp-bridge/cmd/cli@latest
```

## Development

Run `mise install` for the pinned toolchain, then `just verify` before committing.
See `AGENTS.md` for conventions.

## Licence

Apache 2.0. See `LICENSE`.
