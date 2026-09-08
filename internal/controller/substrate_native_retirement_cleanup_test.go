package controller

import (
	"errors"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
	"github.com/orka-agents/orka/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNativeSubstrateDrainPreservesOriginalSessionCleanup(t *testing.T) {
	for _, operation := range []string{"scale-down", "rollout", "suspend"} {
		for _, failure := range []string{"none", "receipt write failure", "Task boot mismatch", "live descendant"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				h := newNativeRuntimeTestHarness(t)
				h.until(t, nativeTestServing)
				original := h.record(t)
				current := runtimePoolTestGetPool(t, h.r, h.pool)
				task := runtimePoolRetirementTask(t, &current, "native-turn")
				var wantErr error
				if failure == "Task boot mismatch" {
					task.Status.Execution.RuntimeSessionSupervisorBootID = "foreign-boot"
					wantErr = store.ErrConflict
				}
				if err := h.r.Create(t.Context(), task); err != nil {
					t.Fatal(err)
				}
				if operation == "rollout" {
					// A new controller epoch rotates the Actor while retaining
					// the old instance's frozen cleanup authority.
					h.r.ControllerEpoch++
				} else {
					current.Spec.DesiredReplicas = 0
					if operation == "suspend" {
						current.Annotations[runtimePoolWorkspaceSuspendAnnotation] = booleanTrueValue
					}
					current.Generation++
					if err := h.r.Update(t.Context(), &current); err != nil {
						t.Fatal(err)
					}
				}
				h.until(t, func(*corev1alpha1.RuntimePool, *substrateNativeState) bool {
					return h.supervisor.drainCalls > 0
				})
				h.supervisor.probe.Status.Lifecycle = harnessv2.SupervisorLifecycleDraining
				h.supervisor.probe.Status.Drain = harnessv2.DrainStatus{
					Requested: true, Reason: h.supervisor.drainReason, RequestedAt: runtimePoolTestNow,
				}
				if failure == "live descendant" {
					h.supervisor.probe.Status.Pressure.LiveDescendants = 1
				}
				originalClient := h.r.Client
				if failure == "receipt write failure" {
					wantErr = errors.New("injected native retirement receipt failure")
					h.r.Client = &providerRetirementReceiptFailureClient{
						Client: originalClient, err: wantErr,
					}
				}
				_, err := h.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(h.pool)})
				if !errors.Is(err, wantErr) {
					t.Fatalf("native %s with %s = %v, want %v", operation, failure, err, wantErr)
				}
				current = runtimePoolTestGetPool(t, h.r, h.pool)
				if err := h.r.Get(t.Context(), client.ObjectKeyFromObject(task), task); err != nil {
					t.Fatal(err)
				}
				if failure != "none" {
					if current.Status.ActiveInstance == nil || current.Status.Lifecycle == corev1alpha1.RuntimePoolLifecycleQuiescent ||
						h.record(t).Pending != nil || h.api.suspends != 0 || h.api.deletes != 0 {
						t.Fatal("unproved Task cleanup advanced native runtime retirement")
					}
					if task.Status.Execution.RuntimeSessionCleanupDigest != "" {
						t.Fatal("unproved Task cleanup produced a receipt")
					}
					h.r.Client = originalClient
					task.Status.Execution.RuntimeSessionSupervisorBootID = original.Attempt.BootID
					if err := h.r.Status().Update(t.Context(), task); err != nil {
						t.Fatal(err)
					}
					h.until(t, func(pool *corev1alpha1.RuntimePool, _ *substrateNativeState) bool {
						return pool.Status.Lifecycle == corev1alpha1.RuntimePoolLifecycleQuiescent
					})
				}
				if err := h.r.Get(t.Context(), client.ObjectKeyFromObject(task), task); err != nil {
					t.Fatal(err)
				}
				if !runtimeSessionCleanupCompleteForUID(task, task.UID) {
					t.Fatal("native drain reached quiescence without preserving original Session cleanup")
				}
				if operation == "rollout" {
					h.until(t, nativeTestServing)
				} else {
					h.until(t, func(pool *corev1alpha1.RuntimePool, _ *substrateNativeState) bool {
						return pool.Status.Lifecycle == corev1alpha1.RuntimePoolLifecycleStopped && pool.Status.ActiveInstance == nil
					})
				}
				if h.api.actors[original.Attempt.Name] != nil {
					t.Fatal("native fixture did not retire the original Actor")
				}
				worker := original.Attempt.Worker
				if err := h.r.Get(t.Context(), client.ObjectKey{Namespace: worker.Namespace, Name: worker.Pod}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
					t.Fatalf("original worker Pod after native retirement = %v", err)
				}
				dispatcher := &ACPDispatcher{Client: h.r.Client, APIReader: h.r.Client}
				if ready, err := dispatcher.reconcileRecoveredRuntimeSession(t.Context(), task, task.UID, true, &sessionRuntimeCleanupFence{}); err != nil || !ready {
					t.Fatalf("original Session cleanup after native %s = %v, %v", operation, ready, err)
				}
			})
		}
	}
}
