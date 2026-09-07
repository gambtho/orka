package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	workspacev1alpha1 "github.com/orka-agents/orka/api/workspace/v1alpha1"
)

func TestNativeSubstrateUpgradeDrainPreservesPendingDetachCheckpoint(t *testing.T) {
	h := newNativeRuntimeTestHarness(t)
	h.until(t, nativeTestServing)
	source := h.record(t).Attempt
	h.api.data[source.Name] = "workspace change before controller restart"

	pool := runtimePoolTestGetPool(t, h.r, h.pool)
	linked := &workspacev1alpha1.ExecutionWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pool.Namespace, Name: "pending-upgrade-suspend", UID: "pending-upgrade-uid",
			Annotations: map[string]string{
				acpWorkspaceDetachActionAnnotation:  string(workspacev1alpha1.WorkspaceOnDetachSuspend),
				acpExecutionWorkspacePoolAnnotation: pool.Name,
			},
		},
		Spec: workspacev1alpha1.ExecutionWorkspaceSpec{
			Mode:         workspacev1alpha1.ExecutionWorkspaceModeInteractive,
			DesiredState: workspacev1alpha1.ExecutionWorkspaceDesiredReady,
		},
	}
	require.NoError(t, h.r.Create(t.Context(), linked))
	pool.Labels[acpExecutionWorkspaceLinkLabel] = linked.Name
	pool.Annotations[acpExecutionWorkspaceUIDAnnotation] = string(linked.UID)
	require.NoError(t, h.r.Update(t.Context(), &pool))

	// The real restart hook lowers replicas before Task settlement records
	// DesiredState=Suspended. Its frozen detach action must preserve the data
	// while the coordinator drains the supervisor and settlement catches up.
	coordinator := &ACPUpgradeDrainCoordinator{Client: h.r.Client}
	require.NoError(t, coordinator.setRuntimePoolDesiredReplicasZero(t.Context(), client.ObjectKeyFromObject(&pool)))
	for range 16 {
		h.step(t)
	}
	require.Contains(t, h.api.actors, source.Name, "upgrade drain deleted the Actor before its requested checkpoint")
	require.Zero(t, h.api.deletes)
	require.Equal(t, "workspace change before controller restart", h.api.data[source.Name])
	pool = runtimePoolTestGetPool(t, h.r, h.pool)
	require.NotNil(t, pool.Status.ActiveInstance, "planned drain must still be able to authenticate the live supervisor")
	require.EqualValues(t, 1, pool.Status.CurrentReplicas)
	require.Equal(t, corev1alpha1.RuntimePoolAdmissionDraining, pool.Status.AdmissionState)

	// The next controller recovers the Task outcome, then the workspace
	// adapter persists suspension against the still-owned runtime.
	h.r.ControllerEpoch++
	require.NoError(t, h.r.Get(t.Context(), client.ObjectKeyFromObject(linked), linked))
	linked.Spec.DesiredState = workspacev1alpha1.ExecutionWorkspaceDesiredSuspended
	require.NoError(t, h.r.Update(t.Context(), linked))
	adapter := &ACPExecutionWorkspaceAdapterReconciler{Client: h.r.Client, APIReader: h.r.APIReader}
	_, err := adapter.reconcileSuspension(t.Context(), linked)
	require.NoError(t, err)
	h.until(t, nativeTestSuspended)
	saved := h.record(t).Checkpoint
	require.Equal(t, source.UID, saved.SourceUID)
	require.Equal(t, "workspace change before controller restart", h.api.tagData[saved.Name])
	require.Empty(t, h.api.actors)
	require.False(t, h.api.deleteWithLivePod)

	require.NoError(t, h.r.Get(t.Context(), client.ObjectKeyFromObject(linked), linked))
	_, err = adapter.reconcileSuspension(t.Context(), linked)
	require.NoError(t, err)
	require.NoError(t, h.r.Get(t.Context(), client.ObjectKeyFromObject(linked), linked))
	require.Equal(t, workspacev1alpha1.ExecutionWorkspaceStateSuspended, linked.Status.State)
}

func TestLinkedWorkspaceFrozenSuspendHoldRequiresLiveExactWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*workspacev1alpha1.ExecutionWorkspace)
		hold     bool
		deleting bool
	}{
		{name: "pending settlement", hold: true},
		{name: "delete action", mutate: func(w *workspacev1alpha1.ExecutionWorkspace) {
			w.Annotations[acpWorkspaceDetachActionAnnotation] = string(workspacev1alpha1.WorkspaceOnDetachDelete)
		}},
		{name: "replacement workspace", mutate: func(w *workspacev1alpha1.ExecutionWorkspace) {
			w.UID = "replacement-uid"
		}},
		{name: "failed workspace", mutate: func(w *workspacev1alpha1.ExecutionWorkspace) {
			w.Status.State = workspacev1alpha1.ExecutionWorkspaceStateFailed
		}},
		{name: "delete requested", mutate: func(w *workspacev1alpha1.ExecutionWorkspace) {
			w.Spec.DesiredState = workspacev1alpha1.ExecutionWorkspaceDesiredDeleted
		}},
		{name: "deleting workspace", deleting: true, mutate: func(w *workspacev1alpha1.ExecutionWorkspace) {
			w.Finalizers = []string{"test.orka.ai/finalizer"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newNativeRuntimeTestHarness(t)
			pool := runtimePoolTestGetPool(t, h.r, h.pool)
			pool.Labels[acpExecutionWorkspaceLinkLabel] = "frozen-suspend"
			if pool.Annotations == nil {
				pool.Annotations = map[string]string{}
			}
			pool.Annotations[acpExecutionWorkspaceUIDAnnotation] = "original-uid"
			linked := &workspacev1alpha1.ExecutionWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: pool.Namespace, Name: "frozen-suspend", UID: "original-uid",
					Annotations: map[string]string{acpWorkspaceDetachActionAnnotation: string(workspacev1alpha1.WorkspaceOnDetachSuspend)},
				},
				Spec: workspacev1alpha1.ExecutionWorkspaceSpec{
					Mode:         workspacev1alpha1.ExecutionWorkspaceModeInteractive,
					DesiredState: workspacev1alpha1.ExecutionWorkspaceDesiredReady,
				},
			}
			if tc.mutate != nil {
				tc.mutate(linked)
			}
			require.NoError(t, h.r.Create(t.Context(), linked))
			if tc.deleting {
				require.NoError(t, h.r.Delete(t.Context(), linked))
			}
			hold, err := h.r.linkedWorkspaceSuspendIntentPending(t.Context(), &pool)
			require.NoError(t, err)
			require.Equal(t, tc.hold, hold)
		})
	}
}
