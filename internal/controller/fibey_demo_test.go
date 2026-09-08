/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package controller

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
	"github.com/orka-agents/orka/workers/acp/supervisor"
	sigsyaml "sigs.k8s.io/yaml"
)

// Decode the shipped files instead of reproducing their contents in a fixture.
// Strict API decoding alone does not exercise runtimeRef compatibility rules.
func TestFibeyDemoRuntimeRefCompatibility(t *testing.T) {
	directory := filepath.Join("..", "..", "examples", "fibey-custom-agent-demo")
	for _, backend := range []string{"agentkit", "foundry"} {
		t.Run(backend, func(t *testing.T) {
			var task corev1alpha1.Task
			var agent corev1alpha1.Agent
			for name, target := range map[string]any{
				"task.yaml":                  &task,
				"agent-" + backend + ".yaml": &agent,
			} {
				data, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := sigsyaml.UnmarshalStrict(data, target); err != nil {
					t.Fatalf("decode %s: %v", name, err)
				}
			}
			task.Spec.AgentRef.Name = agent.Name
			reconciler := &TaskReconciler{}
			if err := reconciler.validateTaskAgentCompatibility(&task, &agent); err != nil {
				t.Fatalf("demo Task cannot use its runtimeRef Agent: %v", err)
			}
			if err := validateHarnessV2RuntimeRefAgentTaskRestrictions(&task, &agent); err != nil {
				t.Fatalf("demo overrides its external v2 runtime profile: %v", err)
			}
		})
	}
}

// Exercise the exporter on the actual abbreviated templates, then validate its
// output with the controller's canonical profile and MCP policy checks.
func TestFibeyDemoRegistrationExportCompatibility(t *testing.T) {
	toolDigest, err := harnessv2.CanonicalRuntimeToolPolicyDigest([]string{}, []string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	approvalDigest, err := harnessv2.CanonicalMCPApprovalPolicyDigest(harnessv2.MCPApprovalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	mcpDigest, err := harnessv2.CanonicalMCPConfigurationDigest([]string{})
	if err != nil {
		t.Fatal(err)
	}
	const imageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for key, value := range map[string]string{
		supervisor.EnvModel: "gpt-test", supervisor.EnvWorkspaceIntent: "read",
		supervisor.EnvRuntimeInstanceID: "fixture-instance", supervisor.EnvModelContextLimit: "128000", supervisor.EnvModelOutputLimit: "16384",
		supervisor.EnvAgentKitAdapterDigest: imageDigest, supervisor.EnvFoundryAdapterDigest: imageDigest,
		supervisor.EnvAgentConfigurationDigest: imageDigest, supervisor.EnvToolPolicyDigest: toolDigest,
		supervisor.EnvApprovalPolicyDigest: approvalDigest, supervisor.EnvMCPConfigurationDigest: mcpDigest,
		supervisor.EnvProxyCredentialRole: "operator-managed", supervisor.EnvProxyCredentialScope: "external-runtime", supervisor.EnvResourceClass: "external",
	} {
		t.Setenv(key, value)
	}
	for _, backend := range []string{"agentkit", "foundry"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv(supervisor.EnvProvider, backend)
			input, err := os.ReadFile(filepath.Join("..", "..", "examples", "fibey-custom-agent-demo", "agentruntime-"+backend+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := supervisor.ExportRegistrationFromEnv(bytes.NewReader(input), &output); err != nil {
				t.Fatal(err)
			}
			var registration corev1alpha1.AgentRuntime
			if err := sigsyaml.UnmarshalStrict(output.Bytes(), &registration); err != nil {
				t.Fatal(err)
			}
			if err := validateAgentRuntimeSpec(&registration); err != nil {
				t.Fatalf("exported %s registration is invalid: %v", backend, err)
			}
			if err := registration.Spec.Capabilities.ValidateStrictWorkspaceIntent(corev1alpha1.WorkspaceIntentRead); err != nil {
				t.Fatalf("exported registration cannot serve the demo: %v", err)
			}
		})
	}
}
