# Build and register the Fibey runtimes

This guide is for operators provisioning the two backends. Complete their
registration before running the [demo](README.md#connect-the-agents).

Build one Fibey AgentKit image, then use its digest for both direct ACP and the
Foundry-hosted wrapper. These commands use the `remote-vm` builder and
`linux/amd64`. They publish images but do not deploy workloads.

Use AgentKit source containing commit `c9a18070363b9eded0be8ca826d8fc5365afc828`
and Foundry runtime source containing
`57988dbe9d6b9a5ed2a39ce10b3d5abc8a68a7c7`, or successors with the same contracts.
The Orka checkout must contain the external-v2 integration from PR #487.
Configure the source directories and your writable registry:

```bash
ORKA_DIR="$PWD"
AGENTKIT_DIR="$HOME/projects/agentkit.feat-orka-harness-v2"
FOUNDRY_DIR="$HOME/projects/agent-runtime-foundry.feat-harness-v2"
FIBEY_DEMO="$ORKA_DIR/examples/fibey-custom-agent-demo"
FIBEY_BUILD_DIR="$(mktemp -d)"
FIBEY_REGISTRY=docker.io/sozercan
FIBEY_BUILDER=remote-vm
```

## Build the shared AgentKit image

[agentkitfile.yaml.example](agentkitfile.yaml.example) selects Microsoft Agent
Framework, an OpenAI-compatible model, and the Fibey instructions. Change the
model and endpoint to ones available to your hosted container before building.
The file names `OPENAI_API_KEY` but contains no credential value. Keep direct
tools, `brokeredTools`, and context providers absent for this shared demo.

Build the frontend and framework adapter from the required AgentKit source.
Capture each published digest so a remote builder can resolve the exact inputs:

```bash
docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -t "$FIBEY_REGISTRY/agentkit:fibey-v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/frontend.json" "$AGENTKIT_DIR"
FIBEY_FRONTEND="$FIBEY_REGISTRY/agentkit@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/frontend.json")"

docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$AGENTKIT_DIR/runtimes/microsoft-agent-framework/Dockerfile" \
  -t "$FIBEY_REGISTRY/agentkit-serve-maf:fibey-v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/adapter.json" "$AGENTKIT_DIR"
FIBEY_ADAPTER="$FIBEY_REGISTRY/agentkit-serve-maf@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/adapter.json")"

docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$FIBEY_DEMO/agentkitfile.yaml.example" \
  --build-arg BUILDKIT_SYNTAX="$FIBEY_FRONTEND" \
  --build-arg adapter="$FIBEY_ADAPTER" \
  -t "$FIBEY_REGISTRY/fibey-agentkit:v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/agent.json" "$FIBEY_DEMO"
FIBEY_IMAGE="$FIBEY_REGISTRY/fibey-agentkit@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/agent.json")"
```

## Direct AgentKit ACP backend

Compose Orka's supervisor onto that immutable image:

```bash
docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$ORKA_DIR/workers/acp/images/agentkit/Dockerfile" \
  --build-arg AGENTKIT_RUNTIME_IMAGE="$FIBEY_IMAGE" \
  --build-arg AGENTKIT_ADAPTER_DIGEST="${FIBEY_IMAGE##*@}" \
  -t "$FIBEY_REGISTRY/orka-acp-agentkit:fibey-v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/direct.json" "$ORKA_DIR"
FIBEY_DIRECT_IMAGE="$FIBEY_REGISTRY/orka-acp-agentkit@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/direct.json")"
```

Deploy `FIBEY_DIRECT_IMAGE` with Deployment and Service name
`fibey-agentkit-runtime`, one replica, `strategy.type: Recreate`, and container
name `supervisor`. Drain the registered instance and settle its active Tasks
before replacing it.
The image starts `orka-acp-runtime` and sets `ORKA_ACP_PROVIDER=agentkit`.
The supervisor launches the child with `--protocol acp` and supplies its
loopback model proxy and MCP server. `AGENTKIT_PROTOCOL=orka` selects v1 and
must not be used for this service.

Follow the [AgentKit v2 deployment contract](https://github.com/sozercan/agentkit/blob/c9a18070363b9eded0be8ca826d8fc5365afc828/docs/orka.md#harness-v2-byo-runtime)
and Orka's [supervisor image requirements](../../workers/acp/images/README.md#runtime-contract).
Supply the `ORKA_ACP_*` model, profile, epoch, instance, pool UUID/generation,
trust namespace, and Secret-mounted authentication files. Configure the Orka
MCP/artifact endpoints and upstream model proxy as documented there. A writable
`/sessions` volume and the supervisor's process/identity permissions are required;
the child runs under a separate UID/GID. The composed image alone is not a
configured v2 service.

## Host the same AgentKit agent in Foundry

Build [Dockerfile.foundry](Dockerfile.foundry) with the AgentKit repository as
its context. It copies AgentKit's real hosted wrapper onto the same Fibey image:

```bash
docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$FIBEY_DEMO/Dockerfile.foundry" \
  --build-arg AGENTKIT_IMAGE="$FIBEY_IMAGE" \
  -t "$FIBEY_REGISTRY/fibey-agentkit-foundry:v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/hosted-agent.json" "$AGENTKIT_DIR"
FIBEY_HOSTED_IMAGE="$FIBEY_REGISTRY/fibey-agentkit-foundry@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/hosted-agent.json")"
```

Deploy this digest as a concrete Foundry Hosted Agent version. Use the
[AgentKit hosted-agent deployment example](https://github.com/sozercan/agentkit/blob/c9a18070363b9eded0be8ca826d8fc5365afc828/test/foundry-hosted-agent/README.md#deploy-to-foundry-with-azd)
and its `foundry.agent.yaml.example`, selecting the `responses` protocol and
port `8088`. Configure the model credential at deployment time. The wrapper
uses the actual AgentKit framework and baked config, not a mock model.

The wrapper relies on Foundry's Entra-authenticated ingress. Keep it behind
that ingress. Ordinary `agentkit-serve --protocol foundry` with a public bind
requires an additional AgentKit bearer; the current Foundry broker does not
inject that bearer. The dedicated wrapper avoids that incompatible extra
authentication layer. Its `/responses` output is non-streaming JSON, which the
Foundry ACP bridge accepts.

Record the project endpoint, agent name, and exact version returned by your
deployment. Do not use `latest` as the version. Provision the Foundry broker's
Azure identity with access to that target using the deployment's normal identity
configuration.

## Build the Foundry ACP bridge

Copy [foundry-acp.json](foundry-acp.json) into the Foundry build context and edit
its public target fields and model to match the deployment. This file contains
no Azure credentials. Its exact bytes will be part of the v2 profile:

```bash
cp "$FIBEY_DEMO/foundry-acp.json" "$FOUNDRY_DIR/examples/fibey-acp.json"
# Edit $FOUNDRY_DIR/examples/fibey-acp.json with the deployed target before building.

docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$FOUNDRY_DIR/Dockerfile.acp" \
  --build-arg FOUNDRY_CONFIG=examples/fibey-acp.json \
  -t "$FIBEY_REGISTRY/fibey-foundry-acp:v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/foundry-source.json" "$FOUNDRY_DIR"
FIBEY_FOUNDRY_SOURCE="$FIBEY_REGISTRY/fibey-foundry-acp@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/foundry-source.json")"

docker buildx build --builder "$FIBEY_BUILDER" --platform linux/amd64 \
  -f "$ORKA_DIR/workers/acp/images/foundry/Dockerfile" \
  --build-arg FOUNDRY_RUNTIME_IMAGE="$FIBEY_FOUNDRY_SOURCE" \
  --build-arg FOUNDRY_ADAPTER_DIGEST="${FIBEY_FOUNDRY_SOURCE##*@}" \
  -t "$FIBEY_REGISTRY/orka-acp-foundry:fibey-v2" --provenance=false --push \
  --metadata-file "$FIBEY_BUILD_DIR/foundry-runtime.json" "$ORKA_DIR"
FIBEY_FOUNDRY_IMAGE="$FIBEY_REGISTRY/orka-acp-foundry@$(jq -er '."containerimage.digest"' "$FIBEY_BUILD_DIR/foundry-runtime.json")"
```

Use a single-replica Deployment named `fibey-foundry-runtime` with
`strategy.type: Recreate` and these containers, following the
[Foundry v2 process configuration](https://github.com/orka-agents/agent-runtime-foundry/blob/57988dbe9d6b9a5ed2a39ce10b3d5abc8a68a7c7/docs/harness-v2.md#process-configuration):

| Container | Image | Process |
| --- | --- | --- |
| `supervisor` | `FIBEY_FOUNDRY_IMAGE` | Default `orka-acp-runtime` entrypoint, with `ORKA_ACP_PROVIDER=foundry` |
| `broker` | `FIBEY_FOUNDRY_SOURCE` | `/agent-runtime-foundry --protocol broker --config /agent/foundry.json` |

The supervisor needs the same classes of v2 bootstrap configuration as the
direct backend. Point `ORKA_ACP_PROVIDER_PROXY_BASE_URL` at
`http://127.0.0.1:8091/v1`; its provider token file holds the broker bearer.
The broker listens only on loopback and owns a private persistent volume.
Only the broker receives Azure Workload Identity or another refreshable
`DefaultAzureCredential` configuration. The broker's state must survive both
container and Pod replacement and remain inaccessible to the ACP child.

Expose the supervisor as `fibey-foundry-runtime`.

## Register the deployed backends

Create the two `AgentRuntime` registrations as part of backend provisioning,
following the [external runtime contract](../../website/docs/guides/bring-your-own-agent-runtime.md#strict-governed-registration)
and its complete registration sample. The deployment must pin the exact
identity, profile, protocol limits, and governance claims of each service:

| Registration | `providerKind` | `adapterName` | Adapter digest | Agent configuration digest |
| --- | --- | --- | --- | --- |
| `fibey-agentkit-runtime` | `agentkit` | `agentkit-serve-acp` | Digest of the Fibey AgentKit source image, before supervisor composition | SHA-256 of the exact baked `/agent/agent.yaml` bytes |
| `fibey-foundry-runtime` | `foundry` | `foundry-serve-acp` | Digest of the configured Foundry ACP source image, before supervisor composition | SHA-256 of the exact baked `/agent/foundry.json` bytes |

Both profiles use read intent, credential role `operator-managed`, credential
scope `external-runtime`, and resource class `external`. The model must match
the baked configuration. Keep all MCP tool and approval lists empty and
`allowBash: false`. The runtime's `/v2/capabilities` must advertise
`supportsAgentSessionConfiguration: false`; that is an HTTP capability, not an
additional field in the AgentRuntime CRD.

Do not hash the Agentkitfile as the AgentKit configuration digest. AgentKit
renders that input into a different `/agent/agent.yaml` file. The overall
profile digest must use the v2 contract's canonicalization.

Provision separate controller-bearer and operation-capability Secrets for each
registration, with values of at least 32 bytes. Both Secrets need the exact
registration name and endpoint bindings described in the external runtime
contract. Mount them into the corresponding supervisor. Keep model credentials
separate; only the Foundry broker receives Azure identity and its private
ownership ledger.

Pin each registration to its supervisor lifetime's instance ID and configure
`ORKA_ACP_CONTROLLER_EPOCH` with the current controller epoch. Handle epoch and
instance changes through the external runtime lifecycle. Do not replace a
Foundry lifetime or delete its ledger while remote ownership remains unresolved.

Wait for the actual `AgentRuntime` registrations to become Ready before running
the demo. `GET /v2/health` and `GET /v2/capabilities` are safe public probes;
readiness also requires authenticated status and Orka's conformance checks.
A ready hosted container alone does not establish that the bridge can
authenticate or settle remote work.
