# Upstream Substrate execution

Orka's Substrate integration targets the unmodified upstream provider. The
provider image, protocol source, installer, and conformance suite must agree on
the commit recorded in `hack/agent-substrate/upstream.env`. Fork-only APIs and
local provider patches are outside this contract.

## Required behavior

- Native ate-api Actor, ActorTemplate, Atespace, Worker, and Tag resources.
- Authenticated control calls with verified TLS and rotating client credentials.
- Immutable compiled templates with explicit placement, readiness, capabilities,
  resource limits, durable-volume policy, and provider admission limits.
- Direct workspaces, MCP actor pools, and workspace-backed ACP Tasks on the same
  provider version, including cancellation, timeout, replacement, and cleanup.
- Durable Session identity independent of active compute. Requested suspension
  preserves data and cold-boots with fresh credentials on continuation.
- Checkpoints and forks use upstream immutable Tags, verify snapshot scope and
  provenance, and record recovery state before reporting success.
- Runtime diagnostics distinguish connectivity, admission, execution, and
  recovery capabilities, with actionable errors for incompatible providers.
- No raw credentials in provider templates, snapshots, status, or logs; no
  process-memory restoration; no automatic replay of uncertain external effects.

## Implementation order

1. Regenerate the upstream protocol reproducibly and migrate control calls,
   authentication, identity, namespace routing, and paginated inventory.
2. Migrate native immutable template compilation, ownership, and cleanup; verify
   ordinary ACP, direct, and MCP execution against the unmodified provider.
3. Implement data-only checkpoint, cold continuation, and fork with explicit
   Orka operation records and end-to-end runtime identity verification. Do not
   claim that a preflight read makes an unfenced provider mutation atomic.
4. Add diagnostics and upgrade conformance, remove obsolete provider patches,
   update public configuration and lifecycle documentation.
5. Run focused tests, full required checks, local provider conformance,
   independent review, and PR checks.

## Recovery constraints

Upstream Suspend/Resume requests do not accept caller UID/version preconditions
or operation IDs. Tags capture the source Actor's snapshot when tag creation
executes and may seed only an Actor using the same immutable template. Therefore
Orka must verify the Tag's source Actor UID, template UID, and data-only content,
keep native lifecycle operations under one durable controller-owned operation,
and bind restored runtime admission to fresh credentials and an exact boot.
Any unresolved ambiguity preserves the checkpoint and closes admission.

Data-only snapshot success alone does not prove workload termination. Cleanup
and suspended status require an independent termination proof. A failed
checkpoint, interrupted restore, replaced Actor, or ambiguous create must never
be reported as successful recovery.

## Acceptance coverage

Conformance must exercise real upstream protocol validation and authentication,
native template creation and immutable conflicts, ordinary execution, dormant
history reads, suspend/continue, checkpoint/fork, changed credentials, controller
restart, duplicate requests, failed cleanup, stale identities, and long quiet
streams. Unit fixtures must not implement capabilities absent from upstream.
