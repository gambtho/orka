# Scope: honor Agent model settings in the AI worker

Status: approved for implementation, including the second-opinion
environment-ownership finding and the non-positive maxTokens compatibility policy.

Baseline: `516fdf73f16c2285a74f34d2525657c305c05578`
(`fix(examples): update workflows and resolve live validation blockers (#519)`).

## Goal

For Tasks executed by the built-in `type: ai` worker, honor
`Agent.spec.model.temperature` and `Agent.spec.model.maxTokens` on every model
call. Preserve existing defaults when settings are absent. Implement the fields
rather than removing them or documenting them as unsupported.

The motivating Agent declares `temperature: 0` and `maxTokens: 256`; its provider
request must contain an explicit zero temperature and a 256-token output limit.
This guarantees request configuration, not deterministic model output.

## Confirmed current behavior

- `api/v1alpha1/agent_types.go:181-197`: both settings are optional pointers.
  Temperature permits zero. MaxTokens retains legacy zero and has no minimum
  validation marker.
- `internal/controller/job_builder.go:973-1007,1082-1097`: `aiConfig`,
  `resolveAIConfig`, and `addAIEnvVars` do not carry either setting.
- `internal/workerenv/env.go:451-529`: `AIWorkerEnv`, its environment serializer,
  and parser have no fields for these settings. The worker receives projected
  environment configuration, not the complete Agent object.
- `workers/ai/main.go:1185-1256`: the loop receives no sampling/output settings,
  creates requests with a fixed `MaxTokens: 4096`, and omits temperature. The
  context-overflow retry reuses that request with truncated messages.
- `internal/llm/provider.go:29-36`: `CompletionRequest.Temperature` is a scalar
  `float64`, so its zero value cannot distinguish omission from explicit zero.
- `internal/llm/openai/provider.go:469,940,1022` and
  `internal/llm/anthropic/provider.go:192`: provider adapters serialize temperature
  only when it is greater than zero. Worker plumbing alone will not fix the
  motivating zero-temperature case.
- `internal/llm/tracing.go:457`: request-temperature tracing also omits zero.
- `internal/controller/job_builder.go:757-761,1407-1420`: Task environment values
  are retained before AI configuration is added, and the direct Agent-secret path
  imports arbitrary keys through `EnvFrom`. Optional emission alone would allow
  these sources to supply settings omitted by the Agent. Explicit empty
  controller-owned values have a precedent at `job_builder.go:770-774`, tested
  in `internal/controller/job_builder_test.go:2924-2933`.

These are source-inspection findings, not a live-provider reproduction.

## Required behavior

| Agent setting | Effective AI-worker request |
| --- | --- |
| No Agent, no model block, or both settings absent | 4096-token output limit; temperature omitted |
| Temperature explicitly zero | Explicit temperature zero |
| Valid positive temperature | Exact configured temperature |
| Positive maxTokens, including below or above 4096 | Exact configured output limit |
| MaxTokens omitted, zero, or negative | 4096-token output limit |
| Only one setting supplied | Honor it; retain the other setting's default |

Apply the same resolved settings to initial requests, tool-calling follow-ups,
context-overflow retries, and other loop-generated follow-up requests. Existing
fallback request copies must preserve them when switching model/provider.

Do not silently turn malformed, non-finite, or out-of-range newly supplied
worker-environment settings into defaults. Validate them before the first model
call using the existing worker startup/error path. Do not broaden API validation
or introduce model-specific capability detection in this fix; provider rejection
of an otherwise configured request remains an error, not a reason to silently
remove the setting.

## Intended implementation boundary

1. Extend the existing path: Agent model configuration → controller `aiConfig`
   → shared `AIWorkerEnv` serialization/parsing → explicit loop configuration
   → every `llm.CompletionRequest`. Preserve temperature presence throughout;
   an absent environment value is distinct from the string `"0"`.
   For controller-built AI Jobs, reserve both new environment keys even when
   there is no Agent, model block, or corresponding setting. Replace any
   colliding Task environment entries with controller-owned values; use explicit
   empty values for omitted settings so Agent Secret `EnvFrom` keys cannot fill
   them in. Follow the existing controller-owned environment setters to avoid
   duplicate keys or retaining a Task-supplied `ValueFrom`. The worker must treat
   these explicit empty values as unset, preserving the defaults above. Neither
   Task environment entries nor Agent Secret keys may override configured values
   or supply omitted settings.
2. Make temperature presence expressible in the internal completion request and
   honor it in all existing OpenAI and Anthropic complete/stream serialization
   paths. Preserve omitted temperature and existing positive-temperature behavior
   for other callers. Do not merely replace `> 0` with `>= 0`.
3. Prefer a backward-compatible additive presence indicator alongside the scalar
   temperature, unless review establishes that a pointer migration is safer.
   With an additive indicator, both explicit presence and legacy positive scalar
   values must cause serialization. Audit request copies and any serialization
   boundaries before choosing the representation; scalar `omitempty` alone does
   not preserve explicit zero through JSON round trips.
4. Use the same effective presence semantics in existing request tracing so an
   explicitly configured zero is observable when telemetry is enabled. Do not
   enable telemetry or add prompt/content capture.
5. Keep loop configuration explicit and follow nearby test/caller patterns. Avoid
   unrelated loop refactoring or introducing a new configuration transport.

Expected source areas: `internal/controller/job_builder.go`,
`internal/workerenv/`, `workers/ai/`, and the request type, OpenAI/Anthropic
adapters, and tracing under `internal/llm/`, plus focused tests.

## Compatibility and exclusions

- No CRD, generated-file, persisted-data, or public Task API changes.
- No ACP/RuntimePool, coding-agent CLI, or execution-workspace behavior changes.
- Do not add Task-level sampling overrides or change provider/model precedence.
- Shared LLM adapter changes are necessary, but unrelated chat/compatibility API
  defaulting behavior must remain unchanged; fixing their own zero-presence
  behavior is not part of this worker issue.
- Do not add dependencies, fetch Agents from the worker, expose credentials,
  introduce new status conditions/events, or overhaul telemetry.
- New worker binaries with old Jobs/controllers must retain defaults when the new
  environment settings are absent. This scope does not claim that an old worker
  image will honor settings emitted by a new controller; deployment requires the
  corresponding controller and worker versions.

## Approved implementation decisions

- **Non-positive maxTokens:** omitted, zero, and negative values retain 4096 to
  avoid new failures for existing Agents. Positive values are honored exactly.
  Malformed or unrepresentable environment values still fail validation; this
  compatibility rule is not a reason to ignore parse errors.
- **Presence representation:** use an additive temperature-presence indicator,
  preserving existing positive scalar callers and omitted defaults. Verify
  wrappers and serialization boundaries without migrating unrelated callers.

## Acceptance and verification

Add regression tests that first fail on the existing behavior:

- Controller resolution and Job environment: absent Agent/model/settings,
  explicit zero, positive temperature, independent settings, zero/negative caps,
  and caps both below and above 4096.
- Environment ownership/collisions: for both new keys, cover colliding Task
  literal/`ValueFrom` entries and the direct Agent Secret `EnvFrom` path, with
  omitted and configured Agent settings (including explicit zero temperature).
  Assert the final Job has a single controller-owned explicit entry per key,
  no Task-supplied `ValueFrom`, and an empty value when that setting is omitted;
  Secret imports must not change the effective setting or default.
- Environment round trips: absence and explicit empty values versus `"0"`, exact
  supported values, and startup failure for invalid supplied values under the
  agreed validation policy.
- Worker request capture: defaults and configured values on the first call,
  tool follow-ups, context-overflow retry, and other loop follow-ups.
- Provider serialized request bodies: absent temperature stays absent, explicit
  zero is present, positive legacy scalar callers still work, and output limits
  reach the appropriate OpenAI/Anthropic fields. Cover OpenAI Responses and Chat
  Completions, complete and stream entry points, using shared builders where
  appropriate. Cover Anthropic complete/stream through its shared builder.
- Fallback/request-copy preservation and enabled tracing of explicit zero.

Use the existing controller tests, `workers/ai/main_test.go` mock-provider request
capture, and adapter HTTP/builder tests rather than requiring live credentials.
Run focused tests for changed packages, then repository-required
`make lint-fix && make test` after Go edits. Format before review and perform the
required changed-code review before claiming implementation complete.

Document review was source-only; it did not establish runtime reproduction or
passing implementation tests. Implementation completion requires the verification
above. Generated files remain outside the change scope.
