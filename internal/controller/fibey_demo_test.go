/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package controller

import (
	"os"
	"path/filepath"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
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
