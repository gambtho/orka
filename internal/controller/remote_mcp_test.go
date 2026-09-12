package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRemoteMCPControllerDoesNotHostOrProbe(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	var tool corev1alpha1.Tool
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"remote","namespace":"default"},"spec":{"description":"reviewed","parameters":{"type":"object"},"http":{"authSecretRef":{"name":"auth","key":"token"},"outboundAccessPolicyRef":{"name":"egress"}},"mcp":{"remote":{"url":"`+server.URL+`/mcp","toolName":"read"}}}}`), &tool); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = corev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	policy := &corev1alpha1.OutboundAccessPolicy{ObjectMeta: metav1.ObjectMeta{Name: "egress", Namespace: "default", Generation: 1}}
	if err := json.Unmarshal([]byte(`{"gateway":{"serviceRef":{"name":"gateway","port":8080}}}`), &policy.Spec); err != nil {
		t.Fatal(err)
	}
	policy.Status.ObservedGeneration = 1
	for _, typ := range []string{corev1alpha1.OutboundAccessPolicyConditionAccepted, corev1alpha1.OutboundAccessPolicyConditionResolvedRefs} {
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1})
	}
	statusWrites := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&tool).WithObjects(&tool, policy, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-credential")}}).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if subresource == "status" {
				statusWrites++
			}
			return c.SubResource(subresource).Update(ctx, obj, opts...)
		},
	}).Build()
	r := &ToolReconciler{Client: c, Scheme: scheme, SkipSSRFValidation: true, HTTPClient: server.Client()}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&tool)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(&tool), &tool); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("controller issued %d ambient credential probes", calls.Load())
	}
	accepted := meta.FindStatusCondition(tool.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("remote not accepted: %+v", tool.Status)
	}
	available := meta.FindStatusCondition(tool.Status.Conditions, "Available")
	if tool.Status.Available || available == nil || available.Status != metav1.ConditionUnknown {
		t.Fatalf("controller claimed authenticated readiness: %+v", tool.Status)
	}
	if tool.Status.Actor != nil || tool.Status.Workspace != nil || len(tool.Finalizers) > 0 {
		t.Fatalf("remote gained hosting state: %+v", tool)
	}
	assertRemoteMCPStatusStable(t, r, &tool, &statusWrites)
	// Schemaless admission stays compatible; controller rejection must revoke acceptance.
	tool.Spec.Parameters = nil
	if err := c.Update(t.Context(), &tool); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&tool)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(&tool), &tool); err != nil {
		t.Fatal(err)
	}
	accepted = meta.FindStatusCondition(tool.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionFalse || tool.Status.Error == "" || tool.Status.LastCheck != nil {
		t.Fatalf("invalid remote configuration retained acceptance or fabricated a health check: %+v", tool.Status)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid remote configuration probed endpoint")
	}
	if statusWrites < 2 {
		t.Fatal("changed remote status was not persisted")
	}
	assertRemoteMCPStatusStable(t, r, &tool, &statusWrites)
	before := statusWrites
	tool.Generation++
	if err := c.Update(t.Context(), &tool); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&tool)}); err != nil {
		t.Fatal(err)
	}
	if statusWrites != before+1 {
		t.Fatal("generation change did not update observed conditions")
	}
	assertRemoteMCPStatusStable(t, r, &tool, &statusWrites)
}

func assertRemoteMCPStatusStable(t *testing.T, r *ToolReconciler, tool *corev1alpha1.Tool, statusWrites *int) {
	t.Helper()
	before := *statusWrites
	for range 2 {
		result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tool)})
		if err != nil || result.RequeueAfter != toolHealthCheckInterval {
			t.Fatalf("stable reconciliation lost periodic recheck: %v", err)
		}
	}
	if *statusWrites != before {
		t.Error("unchanged remote status caused additional writes")
	}
}

func TestRemoteMCPExcludedFromACP(t *testing.T) {
	var tool corev1alpha1.Tool
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"remote"},"spec":{"description":"reviewed","brokeredToolClass":"read","parameters":{"type":"object"},"mcp":{"remote":{"url":"https://example.com/mcp","toolName":"read"}}}}`), &tool); err != nil {
		t.Fatal(err)
	}
	if _, err := customACPMCPToolDescriptor(&tool); err == nil {
		t.Fatal("remote tool exposed to ACP")
	}
	tool.Namespace, tool.UID, tool.Generation = "default", "remote-uid", 1
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&tool).Build()
	task := &corev1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec:       corev1alpha1.TaskSpec{AgentRuntime: &corev1alpha1.AgentRuntimeSpec{AllowedTools: []string{tool.Name}}},
	}
	agent := &corev1alpha1.Agent{Spec: corev1alpha1.AgentSpec{Runtime: &corev1alpha1.AgentCLIRuntime{}}}
	target := resolvedHarnessV1Target{
		runtimeRef: &corev1alpha1.AgentRuntime{}, backend: corev1alpha1.AgentExecutionBackendExternalEndpoint,
		supportsContinuation: true, brokeredToolClasses: []corev1alpha1.AgentRuntimeBrokeredToolClass{corev1alpha1.AgentRuntimeBrokeredToolClassRead},
	}
	if _, err := resolveHarnessV1BrokeredTools(t.Context(), reader, task, agent, target); err == nil {
		t.Fatal("remote tool exposed through legacy harness v1 broker")
	}
}
