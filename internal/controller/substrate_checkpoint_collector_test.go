package controller

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	workspacev1alpha1 "github.com/orka-agents/orka/api/workspace/v1alpha1"
	ateapipb "github.com/orka-agents/orka/internal/substratepb"
	"github.com/orka-agents/orka/internal/workspace"
)

type nativeCollectorTestAPI struct {
	ateapipb.ControlClient
	deletedTags, deletedTemplates atomic.Int32
}

func (a *nativeCollectorTestAPI) DeleteTag(
	ctx context.Context, req *ateapipb.DeleteTagRequest, opts ...grpc.CallOption,
) (*ateapipb.Tag, error) {
	value, err := a.ControlClient.DeleteTag(ctx, req, opts...)
	if err == nil {
		a.deletedTags.Add(1)
	}
	return value, err
}

func (a *nativeCollectorTestAPI) DeleteActorTemplate(
	ctx context.Context, req *ateapipb.DeleteActorTemplateRequest, opts ...grpc.CallOption,
) (*ateapipb.ActorTemplate, error) {
	value, err := a.ControlClient.DeleteActorTemplate(ctx, req, opts...)
	if err == nil {
		a.deletedTemplates.Add(1)
	}
	return value, err
}

func nativeCollectorJournals(t *testing.T, h *nativeRuntimeTestHarness) (*corev1.ConfigMap, *corev1.ConfigMap) {
	t.Helper()
	h.until(t, nativeTestServing)
	substrateSuspendTestPoolIntent(t, h.r, h.pool, true)
	h.until(t, nativeTestSuspended)
	catalog, artifact, err := h.r.readSubstrateCheckpointArtifact(t.Context(), h.record(t).Checkpoint.Digest)
	require.NoError(t, err)
	// This is the durable state left after the source pool releases its final
	// reference. The watcher must finish native and Kubernetes collection.
	artifact.Owners, artifact.Deleting = map[string]bool{}, true
	data, err := json.Marshal(artifact)
	require.NoError(t, err)
	catalog.Data[substrateCatalogKey] = string(data)
	bindings := &corev1.ConfigMapList{}
	require.NoError(t, h.r.List(t.Context(), bindings, client.MatchingLabels{
		substrateTemplateBindingLabel: substrateOwnedLabelValue,
	}))
	require.Len(t, bindings.Items, 1)
	template := bindings.Items[0].DeepCopy()
	binding := &substrateTemplateBinding{}
	require.NoError(t, json.Unmarshal([]byte(template.Data["binding.json"]), binding))
	binding.Retired = true
	data, err = json.Marshal(binding)
	require.NoError(t, err)
	template.Data["binding.json"] = string(data)
	for _, object := range []*corev1.ConfigMap{catalog, template} {
		object.ResourceVersion, object.UID = "", ""
	}
	return catalog, template
}

func TestNativeSubstrateCollectorRunsWithoutCheckpointAPI(t *testing.T) {
	h := newNativeRuntimeTestHarness(t)
	catalog, template := nativeCollectorJournals(t, h)
	// No Orka CRDs are installed. A checkpoint watch would prevent this
	// manager from starting, but core ConfigMap watches must still work.
	environment := &envtest.Environment{BinaryAssetsDirectory: getFirstFoundEnvTestBinaryDir()}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: h.r.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	_, err = mgr.GetRESTMapper().RESTMapping(
		workspacev1alpha1.GroupVersion.WithKind("ExecutionWorkspaceCheckpoint").GroupKind(),
	)
	require.True(t, meta.IsNoMatchError(err), "checkpoint API must be absent: %v", err)
	kube, err := client.New(config, client.Options{Scheme: h.r.Scheme})
	require.NoError(t, err)
	require.NoError(t, kube.Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: h.r.ControllerNamespace},
	}))
	require.NoError(t, kube.Create(t.Context(), catalog))
	api := &nativeCollectorTestAPI{ControlClient: h.api}
	pools := &RuntimePoolReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: h.r.Scheme,
		ControllerNamespace: h.r.ControllerNamespace, SubstrateEnabled: false,
		SubstrateNativeClientFactory: func(SubstrateConfig) (*workspace.SubstrateNativeClient, error) {
			return &workspace.SubstrateNativeClient{Control: api}, nil
		},
	}
	collector := &SubstrateCheckpointReconciler{RuntimePools: pools, CheckpointAPIInstalled: false}
	require.NoError(t, collector.SetupWithManager(mgr))
	managerCtx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(managerCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("collector manager did not stop")
		}
	})
	absent := func(object *corev1.ConfigMap) bool {
		return apierrors.IsNotFound(kube.Get(t.Context(), client.ObjectKeyFromObject(object), &corev1.ConfigMap{}))
	}
	require.Eventually(t, func() bool { return absent(catalog) }, 10*time.Second, 50*time.Millisecond)
	require.EqualValues(t, 1, api.deletedTags.Load())
	// Exercise a later watch event as well as startup inventory, after the
	// catalog no longer holds the immutable template revision.
	require.NoError(t, kube.Create(t.Context(), template))
	require.Eventually(t, func() bool { return absent(template) }, 10*time.Second, 50*time.Millisecond)
	require.Positive(t, api.deletedTemplates.Load())
	_, err = collector.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: catalog.Namespace, Name: "unavailable-public-checkpoint",
	}})
	require.NoError(t, err, "public checkpoint requests must not attempt the absent API")
}
