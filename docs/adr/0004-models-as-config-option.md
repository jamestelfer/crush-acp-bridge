# 4. Offer models as a session config option

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

ACP has two ways to offer a model choice: the unstable `models` field on `session/new` with `session/set_model`, and session config options with `session/set_config_option`.

The Go SDK the bridge uses, `github.com/coder/acp-go-sdk` v0.20.0, has no `session/set_model` handler. Paseo selects models through `session/set_config_option`, and calls `session/set_model` only when `session/new` returns the `models` field.

## Decision

The bridge offers Crush's models as one select config option, ID `model`, category `model`. Values are `provider/model`; provider IDs contain no slash. The options list every model of every enabled provider, sorted by provider ID with each provider's own order kept. The current value is Crush's large model; when it is unset, the first model offered; when Crush reports a model it does not list, that model is appended. The `models` field is never sent.

## Consequences

- Paseo lists Crush's models and reports the real model name.
- No client can reach `session/set_model`, so its absence produces no method-not-found.
- A session where Crush lists no models has no model option. A failure to list models is logged and leaves the session without one.
