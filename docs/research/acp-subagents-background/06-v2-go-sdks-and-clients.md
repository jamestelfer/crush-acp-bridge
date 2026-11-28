# ACP v2: Go SDKs and client support

Accessed 2026-11-28. Researched directly by the coordinating session, not by a sub-agent. Marks: **Confirmed** (read in a primary source), **Inferred**, **Unverified**.

## Official SDKs with v2

**Confirmed.** The v2 Preview announcement names Rust, TypeScript, Kotlin and Python. It names no Go SDK:

> Pick an SDK. [Rust] has v2 behind the `unstable_protocol_v2` feature, and [TypeScript] has it under `@agentclientprotocol/sdk/experimental/v2`. Both are updated with each schema release. [Kotlin] and [Python] have v2 APIs too, and we are working on bringing them up to the latest schema.
>
> — https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/announcements/acp-v2-preview.mdx (L96)

The `agentclientprotocol` GitHub organisation has `rust-sdk`, `typescript-sdk`, `python-sdk`, `kotlin-sdk` and `java-sdk`, and no Go SDK (`gh repo list agentclientprotocol`). The spec lists Go SDKs only under community libraries:

> ## Go
> - [acp-go-sdk](https://github.com/coder/acp-go-sdk)
> - [acp-go](https://github.com/ironpark/acp-go)
> - [acp](https://github.com/eino-contrib/acp)
> - [acp-sdk](https://github.com/spachava753/acp-sdk)
> - [acp](https://github.com/Tangerg/acp)
> - [acp-go-sdk](https://github.com/caelis-labs/acp-go-sdk)
> - [acp-go](https://github.com/BrokkAi/acp-go)
>
> — https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/libraries/community.mdx (L36–44)

## Go SDKs and v2

I checked each listed SDK's default-branch tree for v2 paths and read its README and `go.mod` (GitHub API, 2026-11-28).

| SDK | Latest release | v2 | v1 schema | v2 schema | Go / JSON | Stars, contributors | Licence |
|---|---|---|---|---|---|---|---|
| coder/acp-go-sdk (the bridge's) | v0.20.0, 2026-11-18 | **No** (V15) | 1.32.0 | — | `encoding/json/v2` | — | — |
| ironpark/acp-go | v0.7.0, 2026-11-20 | **Yes**, `acp2` package plus `router` | 1.33.0 | **2.0.0-alpha.17** | `go 1.27.0`; README: "uses `encoding/json/v2`" | 36★; 2 contributors (162 + 11 commits) | MIT |
| caelis-labs/acp-go-sdk | v1.9.1, 2026-11-25 | **Yes**, `experimental/v2` | 1.35.0 | **2.0.0-beta.4** (current) | `go 1.23` | 5★; 1 contributor (52 commits) | Apache-2.0 |
| BrokkAi/acp-go | v0.14.0, 2026-11-19 | **Yes**, `schema/v2`, `v2/agent`, routers | 1.33.0 | **2.0.0-alpha.17** | `go 1.27.1` | 2★; 1 contributor (98 commits) | Apache-2.0 |
| eino-contrib/acp | v0.0.4, 2026-06-29 | No | — | — | — | — | — |
| spachava753/acp-sdk | v0.7.0, 2026-11-14 | No | — | — | — | — | — |
| Tangerg/acp | v0.4.0, 2026-11-23 | No | — | — | — | — | — |

The v2 claims, quoted from each README:

- ironpark/acp-go:
  > This is an **unofficial** implementation. ACP v1 is stable; ACP v2 and the HTTP transport specification are drafts.
  > | [`acp2`](acp2/) | Work with draft ACP v2; APIs may change. |
  > | [`router`](router/) | Serve both protocol versions, or connect with v2 → v1 fallback. |
- caelis-labs/acp-go-sdk:
  > `schema.unstable.json` and draft `experimental/v2` are strictly isolated from the root package.
  > Every release must pass a live **99-case** conformance gate across **both** ACP wire profiles (`v1` and `v2`) against the pinned official TypeScript and Rust SDK peers
  >
  > | **Draft ACP v2 Schema** (`experimental/v2`) | … | `schema-v2.0.0-beta.4` |
- BrokkAi/acp-go:
  > **New in v0.14.0:** behavior tracks Rust `agent-client-protocol` 5.4.0. It adds … `replay_complete`, stable draft-v2 subagent sessions, `session/options` …

  `schema/v2/VERSION` is `schema-v2.0.0-alpha.17`.

**Inferred:**
- Only caelis-labs tracks the current v2 schema, beta.4. ironpark and BrokkAi pin alpha.17, which predates the Preview; the announcement mentions "17 alpha releases" before the betas.
- All three are maintained mostly by one person each. None is endorsed by the spec maintainers beyond being listed.
- The 99-case conformance gate claim was not checked against CI runs (**Unverified**).

## Clients that speak v2

| Client | v2 | Evidence |
|---|---|---|
| Zed 1.30.2 | **Behind the `acp-beta` flag**, default staff-only; falls back to v1 | **Confirmed.** `crates/agent_servers/src/acp.rs` L133: `if cx.has_flag::<AcpBetaFeatureFlag>() { match AcpV2Connection::stdio(...) … Err(error) if error.is::<UnsupportedVersion>() => Some(error)`, then `v1::connect(...)`. Flag defaults in V14 |
| JetBrains | Being built, per the announcement; release status **Unverified** (closed source) | "Since July, the Zed and JetBrains teams have been working hard to build ACP v2 into their products" (announcement L13) |
| Paseo 0.17.5 / 0.18.1 / `main` | **No** | The provider's TS SDK is 0.17.1, which has no `experimental/v2` (V12). The plugin's 1.4.0 copy serves the plugin API, not ACP agents (04) |
| acpx 0.23.0 | **No** | No `experimental/v2` import or `protocolVersion: 2` in the `dist/` of the npm tarball for `acpx@0.23.0` |
| CodeCompanion, agent-shell, avante, Obsidian, Toad, marimo | No evidence of v2 | Not re-checked for v2 specifically. Their SDK versions in 04 predate v2, or they use hand-written JSON-RPC (**Inferred**) |

**Agents** with v2 (not clients): claude-agent-acp and codex-acp "both serve v2 next to v1" (announcement L97; codex-acp in 03). A GitHub code search for `@agentclientprotocol/sdk/experimental/v2` (2026-11-28) returns mostly agents and personal projects, and no widely used client.

## Interop when a v1-only bridge meets a v2 client

**Confirmed** from the migration guide:

> A legacy v1-only Agent can return `"protocolVersion": 1` to a Client requesting v2. The Client continues only if it supports v1, otherwise it disconnects.
>
> — https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v2/migration.mdx (L28)

Zed's fallback code follows this pattern. A v1-only bridge loses nothing with today's clients.

## References

- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/announcements/acp-v2-preview.mdx
- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/libraries/community.mdx
- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v2/migration.mdx
- https://github.com/ironpark/acp-go (README, `go.mod`, `schema/testdata/zod-v2.json`, release v0.7.0)
- https://github.com/caelis-labs/acp-go-sdk (README, `go.mod`, `experimental/v2/`, release v1.9.1)
- https://github.com/BrokkAi/acp-go (README, `go.mod`, `schema/v2/VERSION`, release v0.14.0)
- https://github.com/eino-contrib/acp, https://github.com/spachava753/acp-sdk, https://github.com/Tangerg/acp
- https://github.com/zed-industries/zed/blob/v1.30.2/crates/agent_servers/src/acp.rs
- https://registry.npmjs.org/acpx/-/acpx-0.23.0.tgz
- [05-verification-log.md](05-verification-log.md) V12, V14, V15
