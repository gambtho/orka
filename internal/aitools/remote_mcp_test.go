package aitools

import (
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRemoteMCPSelectionCeiling(t *testing.T) {
	for _, tc := range []string{"selected", "no Agent", "no reference", "disabled", "Task expansion", "runtime Agent", "wrong Agent", "wrong namespace", "non-native Task", "legacy expansion"} {
		t.Run(tc, func(t *testing.T) {
			tool := &corev1alpha1.Tool{ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "team"}, Spec: corev1alpha1.ToolSpec{MCP: &corev1alpha1.MCPToolServer{Remote: &corev1alpha1.RemoteMCPServer{URL: "https://example.com/mcp", ToolName: "read"}}}}
			task := &corev1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: corev1alpha1.TaskSpec{Type: corev1alpha1.TaskTypeAI, AgentRef: &corev1alpha1.AgentReference{Name: "operator"}, AI: &corev1alpha1.AISpec{Tools: []string{"remote", "legacy"}}}}
			agent := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "team"}, Spec: corev1alpha1.AgentSpec{Tools: []corev1alpha1.ToolReference{{Name: "remote"}}}}
			switch tc {
			case "no Agent":
				agent = nil
			case "no reference":
				task.Spec.AgentRef = nil
			case "disabled":
				disabled := false
				agent.Spec.Tools[0].Enabled = &disabled
			case "Task expansion":
				agent.Spec.Tools = nil
			case "runtime Agent":
				agent.Spec.Runtime = &corev1alpha1.AgentCLIRuntime{}
			case "wrong Agent":
				agent.Name = "other"
			case "wrong namespace":
				tool.Namespace = "other"
			case "non-native Task":
				task.Spec.Type = corev1alpha1.TaskTypeAgent
			case "legacy expansion":
				tool.Spec.MCP = nil
				agent = nil
				task.Spec.AgentRef = nil
			}
			err := ValidateRemoteMCPSelection(task, agent, tool)
			wantErr := tc != "selected" && tc != "legacy expansion"
			if (err != nil) != wantErr {
				t.Fatalf("selection error %v; want error %t", err, wantErr)
			}
		})
	}
}

func TestRemoteMCPRequiresReviewedObjectSchemaAtConsumers(t *testing.T) {
	for _, raw := range []string{"", `null`, `true`, `[]`, `{}`, `{"type":"string"}`, `{"type":"object","properties":[]}`} {
		t.Run(raw, func(t *testing.T) {
			tool := &corev1alpha1.Tool{Spec: corev1alpha1.ToolSpec{MCP: &corev1alpha1.MCPToolServer{Remote: &corev1alpha1.RemoteMCPServer{URL: "https://example.com/mcp", ToolName: "read"}}, HTTP: &corev1alpha1.HTTPExecution{AuthSecretRef: &corev1alpha1.SecretKeySelector{Name: "auth", Key: "token"}, OutboundAccessPolicyRef: &corev1alpha1.LocalObjectReference{Name: "egress"}}}}
			if raw != "" {
				tool.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(raw)}
			}
			if err := ValidateRemoteMCPConfiguration(tool); err == nil {
				t.Fatal("accepted missing or malformed reviewed schema")
			}
		})
	}
}
