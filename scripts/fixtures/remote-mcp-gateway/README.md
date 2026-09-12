# Remote MCP gateway — local proof fixture only

This fixture exercises Orka's existing `OutboundAccessPolicy.gateway` transport against a **real, separately deployed MCP server**. It forwards MCP messages; it does not implement `initialize`, `tools/list`, or a tool. Do not deploy it as a production gateway or enable its body logging outside a disposable test environment.

```sh
go test ./scripts/fixtures/remote-mcp-gateway
docker build -t remote-mcp-proof-gateway:local \
  -f scripts/fixtures/remote-mcp-gateway/Dockerfile .
```

## Contract

- Listen on port 8080. `GET /healthz` is an unauthenticated readiness check.
- Mount an operator-generated RSA public key at `/credentials/public.pem` and a nonempty resource Bearer credential at `/credentials/token`, from a separately provisioned named Secret. Never commit private keys or credential values.
- Set `MCP_UPSTREAM` to the fixed cluster-local HTTP authority, e.g. `http://kagent-tools.team-mcp.svc:8084`, without a path. Only original authority `example.com`, path `/mcp`, and POST/DELETE are admitted.
- Require a signed RS256 `txntoken+jwt` with the fixture issuer/audience `remote-mcp-proof`, subject `proof-reader`, workload `native-ai-worker`, nonempty transaction ID, current expiry/issuance, `orka:tools:use`, and context naming namespace `orka-system`, Agent `remote-reader`, Tool `remote-read`, and a Task. These fixture claims are not a production transaction authorization policy.
- Also require the exact mounted resource credential in `Authorization: Bearer …`. Strip `Txn-Token` before upstream forwarding. Discovery and lifecycle methods are allowed; the only allowed call is `k8s_get_resources`.
- Refuse redirects. Record JSON exchanges with credential strings removed and without session/authorization headers. Test data only: bodies can still contain arbitrary application data.

The fixture's authentication unit test proves missing, expired, tampered and incorrectly routed requests do not reach its independently running HTTP backend. A real deployment must use the maintained gateway integration and its transaction/route policy, not this small fixture.

## Recorded native Task proof

Executed in a disposable kind Kubernetes v1.32.2 cluster with:

- Changed Orka controller and AI-worker binaries built from this candidate.
- Independently Helm-installed **kagent-tools 0.2.1**, Kubernetes provider in read-only mode, namespace-scoped to `team-mcp`, with no Secret reads.
- Actual **qwen2.5:3b** through local **Ollama 0.11.8**; no mock model or external model API charge.
- `Tool/remote-read` selecting `k8s_get_resources`, the exact reviewed `tools/list.inputSchema`, `operations-mcp/token`, and `OutboundAccessPolicy/remote-mcp-egress` pointing to this gateway.
- A native `Agent/remote-reader` explicitly enabling the alias and a `type: ai` Task. The test provisioner used the trusted controller Kubernetes identity to create safe Task transaction metadata and an owner-referenced token Secret. This is not a demonstration that an ordinary unauthenticated Task submission creates transaction authority.

Post-review successful Task `remote-proof-final` performed this gateway sequence:

```text
initialize → notifications/initialized → tools/list → DELETE
initialize → notifications/initialized → tools/list → tools/call → DELETE
```

The first session verifies the descriptor before model exposure; the second verifies it again before the call. All nine requests were authenticated at the gateway. The actual call was:

```json
{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"k8s_get_resources","arguments":{"all_namespaces":false,"namespace":"team-mcp","output":"json","resource_name":"remote-proof-target","resource_type":"configmap"}}}
```

The MCP server ran Kubernetes `get configmap remote-proof-target -n team-mcp -o json`, returning text content containing the actual ConfigMap. Its `data.proof` was `remote-mcp-44801bbf8ad7d9e1c88bf59f`, generated separately immediately before this final Task and absent from its prompt. The final Orka result matched exactly:

```json
{"result":"remote-mcp-44801bbf8ad7d9e1c88bf59f"}
```

Exactly **one `tools/call`** occurred for this Task. The proof asserts both that call and the returned value, not just Task phase.

Negative live checks:

| Case | Outcome | MCP calls |
| --- | --- | --- |
| Task adds `remote-read` to an Agent with no enabled remote Tools | Failed before discovery | 0 |
| Task lacks credential-read authority | Failed before discovery | 0 |
| Deliberately mismatched reviewed schema vs actual server descriptor | Failed after discovery, before model exposure | 0 |
| Reachable gateway called without authentication | HTTP 403 | 0 |

The drift case modified the local reviewed schema to create a controlled mismatch; it was **not** a real server-upgrade test. kagent returned text content, so arbitrary-precision structured-result coverage comes from the executor and captured-provider-wire tests, not this live server.

To reproduce, install the reviewed CRDs/controller/worker, deploy the separately operated MCP server and actual model, provision the gateway's named credential and public key, import/review the selected descriptor, and issue an authorized native Task with a fresh hidden ConfigMap value. Assert the exchange and exact result above, then remove the disposable cluster. Pin `--context` on every kubectl invocation and `--kube-context` on every Helm invocation; never rely on the global current context.
