package controller

import (
	"context"
	"errors"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
)

type nativeConsentPatchFailureClient struct {
	client.Client
	err error
}

func (c *nativeConsentPatchFailureClient) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption,
) error {
	if pool, ok := obj.(*corev1alpha1.RuntimePool); ok && pool.Annotations[substrateNativeCheckpointConsent] == "" {
		return c.err
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestNativeSubstrateResumeRetriesConsentClearAfterJournalSave(t *testing.T) {
	h := newNativeRuntimeTestHarness(t)
	h.until(t, nativeTestServing)
	h.api.data[h.record(t).Attempt.Name] = "resume recovery proof"
	substrateSuspendTestPoolIntent(t, h.r, h.pool, true)
	h.until(t, nativeTestSuspended)
	h.step(t)
	pool := runtimePoolTestGetPool(t, h.r, h.pool)
	if pool.Annotations[substrateNativeCheckpointConsent] == "" {
		t.Fatal("stopped runtime lost its checkpoint consent")
	}
	beforeCreates := h.api.creates
	baseClient := h.r.Client
	injected := errors.New("injected consent removal failure")
	h.r.Client = &nativeConsentPatchFailureClient{Client: baseClient, err: injected}
	substrateSuspendTestPoolIntent(t, h.r, h.pool, false)
	var reconcileErr error
	for range 20 {
		_, reconcileErr = h.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(h.pool)})
		if reconcileErr != nil {
			break
		}
	}
	if !errors.Is(reconcileErr, injected) {
		t.Fatalf("resume did not reach the consent write failure: %v", reconcileErr)
	}
	record := h.record(t)
	pool = runtimePoolTestGetPool(t, h.r, h.pool)
	if record.Attempt == nil || pool.Annotations[substrateNativeCheckpointConsent] == "" ||
		pool.Status.AdmissionState == corev1alpha1.RuntimePoolAdmissionAccepting || h.api.creates != beforeCreates {
		t.Fatal("failed consent removal lost the journal barrier or admitted a new runtime")
	}
	attemptName := record.Attempt.Name
	// A new reconciler has only the durable journal and the surviving annotation.
	old := h.r
	h.r = &RuntimePoolReconciler{
		Client: baseClient, APIReader: old.APIReader, Scheme: old.Scheme,
		RuntimeNamespace: old.RuntimeNamespace, ControllerNamespace: old.ControllerNamespace,
		ControllerAPIURL: old.ControllerAPIURL, ControllerAPIPort: old.ControllerAPIPort,
		ControllerEpoch: old.ControllerEpoch, AllowedImages: old.AllowedImages,
		WorkspaceArtifactMaxBytes: old.WorkspaceArtifactMaxBytes,
		ProviderProxy:             old.ProviderProxy, SubstrateEnabled: old.SubstrateEnabled,
		SubstrateConfig: old.SubstrateConfig, SubstrateNativeClientFactory: old.SubstrateNativeClientFactory,
		SubstrateCredentialSeeder: old.SubstrateCredentialSeeder, SupervisorClient: old.SupervisorClient,
		Rand: old.Rand, Now: old.Now,
	}
	h.until(t, nativeTestServing)
	pool = runtimePoolTestGetPool(t, h.r, h.pool)
	if !runtimePoolWorkspaceResumeSettled(&pool, false) || h.record(t).Attempt.Name != attemptName ||
		h.api.creates != beforeCreates+1 || h.api.data[attemptName] != "resume recovery proof" {
		t.Fatal("resumed workspace did not settle with its original attempt and preserved data")
	}
}
