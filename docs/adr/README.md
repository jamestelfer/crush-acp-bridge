# Architecture decision records

Each record states one decision, its context and its consequences. Records are numbered in order and not renumbered. A changed decision gets a new record that supersedes the old one.

| No. | Decision | Status |
|---|---|---|
| [0001](0001-join-crush-shared-server.md) | Join Crush's per-user shared server | Accepted |
| [0002](0002-environment-only-to-local-servers.md) | Send the environment only to local servers | Accepted |
| [0003](0003-bridge-tracks-mode.md) | The bridge tracks the mode it set | Accepted |
| [0004](0004-models-as-config-option.md) | Offer models as a session config option | Accepted |
| [0005](0005-session-close-not-advertised.md) | Do not advertise `session/close` | Accepted |
| [0006](0006-session-load-and-list.md) | Session load, resume and list semantics | Accepted |
| [0007](0007-diff-content-sources.md) | Diff content sources for edit tools | Accepted |
| [0008](0008-turn-failure-on-event-stream-loss.md) | Fail the turn when the event stream drops | Accepted |

Template:

```markdown
# N. Title

- **Status**: Proposed | Accepted | Superseded by [ADR M](…)
- **Date**: YYYY-MM-DD

## Context

## Decision

## Consequences
```
