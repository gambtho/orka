---
slug: /substrate
description: "Agent Substrate execution, data-only suspension, checkpoints, and cold restore."
---

# Agent Substrate workspaces

Orka runs direct workspaces, MCP servers, and built-in ACP runtimes on the
unmodified [Agent Substrate](https://github.com/agent-substrate/substrate)
provider. The supported source and protocol are pinned together in
`hack/agent-substrate/upstream.env`. Provider forks, local patches, and
fork-specific `ActorSnapshot` APIs are not part of this integration.

Substrate owns placement, gVisor isolation, snapshot storage, and physical
workers. Orka owns Task outcomes, durable Sessions and transcripts, prompt
leases, cancellation, runtime admission, workspace data references, and
publication. Reading a dormant Session does not start an Actor.

## Native provider setup

Actor, ActorTemplate, Atespace, Worker, and Tag are native ate-api resources.
Only infrastructure such as WorkerPool is a Kubernetes CRD. Create an
infrastructure template through `kubectl ate create actor-template -f`, with
`metadata.atespace` and `metadata.name`, rather than applying an ActorTemplate
CRD. In Orka's `templateRef`, `namespace` names the native Atespace.

ACP dispatch requires `--substrate-enabled` and
`--acp-workspace-dispatch-enabled`. Class-backed suspension and checkpoint
restore also require `--enable-workspace-provider-api`, Task provenance and
workspace-use admission, and the matching CRDs and webhooks.

Configure these controller connection settings:

| Setting | Purpose |
| --- | --- |
| `--substrate-api-endpoint` | Native TLS gRPC endpoint, usually `api.ate-system.svc:443`. |
| `--substrate-api-ca-file` | Server trust bundle. |
| `--substrate-api-cert-file` and `--substrate-api-key-file` | Client PEM identity, reloaded at each TLS handshake. A projected PodCertificate bundle may supply both paths. |
| `--substrate-api-bearer-token-file` | Alternative to mTLS, reloaded for every RPC. Choose one authentication method. |
| `--substrate-router-url` | In-cluster workload router URL. |
| `--substrate-actor-dns-suffix` | Usually `actors.resources.substrate.ate.dev`. |

The native route is `name.atespace.<suffix>`. Identical Actor names in different
Atespaces remain distinct. An explicit local insecure-TLS option exists, but
the bundled installer uses verified server trust and projected client identity.

The infrastructure template must select exactly one WorkerPool and specify a
gVisor `sandboxConfig`, resource limits, and snapshot storage. The controller
compiles separate immutable native templates for ACP. It preserves admitted
placement and storage settings and supplies the pinned runtime image, process
capabilities, readiness probe, durable volume, and public bootstrap material.
Native revisions and their ownership are recorded in private controller
ConfigMaps. Unused revisions are collected after journal and checkpoint
references disappear.

Back up the controller's lifecycle ConfigMaps along with its Kubernetes state.
An initialized pool with a missing journal closes admission and blocks deletion
until the original journal is restored or an operator completes recovery.
Existing legacy pools with lifecycle records are not automatically migrated.
Finish their cleanup using the previous controller before upgrading.

The WorkerPool must be dedicated to Orka ACP workloads. Orka needs Pod
get/list/delete and NetworkPolicy access in its namespace. It confines worker
egress before delivering credentials. Cross-cluster placement is unsupported.
The router's request timeout must cover the longest supported operation; the
local suite sets `--route-timeout=30m` and uses Envoy info logging. Longer router
shutdown survival also needs a suitable drain timeout and Pod termination grace.

Upstream currently authenticates control clients but does not implement native
resource authorization/RBAC. Treat its control API, router, template operators,
and worker namespace as a trusted infrastructure boundary. Kubernetes `use`
authorization protects Orka workspace and checkpoint selection; it does not add
RBAC to Substrate. Concurrent external mutation of Orka-owned Actors or Tags is
unsupported.

## Execution and cold suspension

`Task.spec.workspace` remains the repository configuration. Clone/read and
publication credentials stay in their existing workspace and publisher
boundaries. They never enter the ACP process tree.

A class-backed Task selects its execution environment independently:

```yaml
spec:
  type: agent
  agentRef: {name: coding}
  sessionRef: {name: work-session, create: true, append: true}
  execution:
    workspace:
      classRef: {name: substrate-coding}
      reusePolicy: session
      onDetach: Suspend
```

The class uses the reserved adapter `acp.workspace.orka.ai/runtime-pool`, a
Substrate `RuntimeProviderConfig`, and a `RuntimeWorkspaceProfile` containing:

```yaml
spec:
  substrate:
    templateRef: {namespace: team, name: coding-infrastructure}
    suspend: {mode: DataOnly}
```

The class must allow Session reuse and Suspend, set an idle timeout or maximum
lifetime, and use Delete deletion policies. See [configuration](../reference/configuration.md)
for the complete class resources.

Each workspace binds a dedicated single-session RuntimePool. On detach:

1. Orka closes admission and authenticates a quiescent supervisor drain.
2. It verifies the exact Actor, worker, and immutable template. The template
   uses `Data` for pause and commit and `ColdBoot` for data restore. Only the
   durable workspace directory participates; runtime credentials, child
   process roots, and process memory remain ephemeral.
3. It drains the single-Actor worker to stop new placement, requests the Data
   snapshot, and captures an independent native Tag. Tag UID, source Actor
   UID/version, original template UID, and observed Data scope are verified.
4. It deletes the exact worker Pod and observes its absence before deleting
   the source Actor and reporting the workspace Suspended.

A successful snapshot alone is never proof that the workload stopped.
Continuation creates a new Actor from the retained Tag using its original
immutable template. Orka then CAS-updates the suspended Actor to the next
compatible template and cold-boots it. The supervisor generates a process-local
X25519 challenge; Orka encrypts and signs bootstrap credentials for that Actor
and challenge before authenticating the new boot. Actor, Pod, boot identity,
and runtime credentials all change.

The native provider has no caller UID/version preconditions on Suspend, Resume,
or Delete. Orka's ConfigMap CAS protects its own operation journal, not provider
mutations. Random non-reused names, immutable template and Tag identities,
explicit creation intents, exact workload deletion, and fresh admission checks
support this cold-only path. An uncertain boot or previously admitted prompt
is not automatically replayed. Full-memory restore remains prohibited by ADR
0030; ADR 0031 replaces the earlier fork-specific DataOnly requirement.

Actor scale-to-zero does not imply WorkerPool Pod scale-to-zero. Upstream
currently provides one Actor slot per worker. Worker capacity and autoscaling
remain operator responsibilities.

## Checkpoints, forks, and recovery

Export a completed checkpoint from an idle suspended workspace:

```yaml
apiVersion: workspace.orka.ai/v1alpha1
kind: ExecutionWorkspaceCheckpoint
metadata:
  name: before-refactor
  namespace: team
spec:
  workspaceRef:
    name: workspace-name
    uid: WORKSPACE_UID
```

Creation requires Kubernetes `use` on the source ExecutionWorkspace. Export
waits for suspension and never interrupts an attached Task. When Ready, the
checkpoint exposes an immutable digest, class revision, and timestamp, without
native identifiers or storage URLs. Its private reference keeps the Data Tag
and original template alive after source workspace deletion.

A fresh Task or Session restores the exact accepted reference:

```yaml
spec:
  execution:
    workspace:
      classRef: {name: substrate-coding}
      reusePolicy: session
      restoreFrom:
        name: before-refactor
        uid: CHECKPOINT_UID
        digest: sha256:CHECKPOINT_DIGEST
```

Restore requires `use` on the checkpoint and the class. Namespace, class and
provider revisions, runtime profile/image, and durable layout must match.
The target acquires its own durable reference before Actor creation. Deleting
the public checkpoint cannot invalidate a restore that already acquired data.
Continuation Tasks in that restored Session must preserve the original
`restoreFrom` binding. To branch from another checkpoint, create a new Session.

The Task fork API accepts `executionCheckpoint` with the same name/UID/digest.
`afterSeq` selects conversation history, not filesystem state. A fork gets an
independent workspace and, for Session reuse, its own Session. Without an
explicit execution checkpoint it starts with fresh workspace data.

After a failed restore or lost Actor, set `recoverLastCheckpoint: true` on a
new export to accept the last verified checkpoint explicitly. Later work may be
missing. Recovery starts a fresh workspace; it does not retry an uncertain
source Task. Removing all pool and public references eventually collects the
Tag, its catalog, and unused templates.

## Diagnostics and verification

`go run ./cmd/orka-substrate-doctor --atespace team --template coding-infrastructure`
uses the `ORKA_SUBSTRATE_API_*` environment settings to check authenticated
native connectivity, Tag inventory, template storage/placement, and worker
readiness. It reports adapter capabilities separately from observed checks and
states that native lifecycle preconditions are absent. It does not identify the
server build or prove execution and streaming compatibility.

Run `bash hack/demos/cluster/install-substrate.sh` for local fixture-backed
conformance on a dedicated gVisor kind cluster. This retains a scoped kubeconfig
under `bin/` and does not modify the default kubeconfig. The source must match
the official pin exactly. Existing clusters require explicit reuse; the
installer does not destroy them to recreate the environment.

The suite exercises native authentication, direct sealed execution and files,
MCP execution, ACP Tasks, controller restart, cold continuation, independent
checkpoint restore, cancellation, timeout, and cleanup. Protocol/TLS and
fault-injection tests additionally cover lost responses, source replacement,
Tag provenance, reference races, and explicit recovery. Local provider
conformance and the PR workflow must pass before treating an upgraded pin as
validated. A doctor pass or unit fixture pass is not live execution evidence.
