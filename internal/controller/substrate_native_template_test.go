package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ateapipb "github.com/orka-agents/orka/internal/substratepb"
	"github.com/orka-agents/orka/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeSubstrateTestRender(t *testing.T, r *RuntimePoolReconciler, nonce string) *unstructured.Unstructured {
	t.Helper()
	pool := runtimePoolSubstrateTestObject()
	cfg, err := r.runtimePoolConfigForDrain(pool)
	if err != nil {
		t.Fatal(err)
	}
	base := substrateTestBaseTemplate()
	spec := base.Object["spec"].(map[string]any)
	delete(spec, "runsc")
	spec["sandboxConfig"] = map[string]any{"sandboxClass": "SANDBOX_CLASS_GVISOR", "configName": "gvisor-default"}
	spec["workerSelector"] = map[string]any{"matchLabels": map[string]any{"orka.ai/worker-pool": "test"}}
	spec["snapshotsConfig"] = map[string]any{"storageLocation": "s3://snapshots/orka"}
	rendered, err := r.renderSubstrateRuntimeTemplate(pool, cfg, base, pool.Spec.ExecutionWorkspace.Substrate.BaseTemplateNamespace, substrateTestActorID(pool), nonce, "public-verification-key")
	if err != nil {
		t.Fatal(err)
	}
	return rendered.object
}

func TestNativeSubstrateCompilerPreservesSupervisorContract(t *testing.T) {
	r, _ := runtimePoolSubstrateTestReconciler(t, nil, &fakeSubstrateActorControl{})
	object := nativeSubstrateTestRender(t, r, "nonce-1")
	native, err := nativeSubstrateRuntimeTemplate(object)
	if err != nil {
		t.Fatal(err)
	}
	container := native.GetContainers()[0]
	if container.GetSecurityContext().GetCapabilities().GetAdd()[0] != "CHOWN" || len(container.GetSecurityContext().GetCapabilities().GetAdd()) != 4 || len(container.GetResources().GetLimits()) != 2 {
		t.Fatal("native template lost the process-identity capabilities or resource limits")
	}
	if container.GetReadyz().GetHttpGet().GetPort() != 80 || len(container.GetEnv()) > 32 {
		t.Fatal("native runtime violates upstream readiness or environment admission")
	}
	if native.GetSnapshotsConfig().GetOnPause() != ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA || native.GetSnapshotsConfig().GetOnCommit() != ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA || native.GetSnapshotsConfig().GetOnResume().GetFromData() != ateapipb.ResumeSource_RESUME_SOURCE_COLD_BOOT {
		t.Fatal("native template could snapshot or restore supervisor memory")
	}
	for _, env := range container.GetEnv() {
		if strings.Contains(env.GetName(), "TOKEN_FILE") || env.GetName() == runtimePoolCapabilitySecretFileEnv {
			t.Fatalf("credential mount survived native compilation: %s", env.GetName())
		}
	}
	other, err := nativeSubstrateRuntimeTemplate(nativeSubstrateTestRender(t, r, "nonce-2"))
	if err != nil {
		t.Fatal(err)
	}
	if other.GetMetadata().GetName() == native.GetMetadata().GetName() {
		t.Fatal("changed bootstrap material reused an immutable template name")
	}
}

func TestNativeSubstrateTemplateDiscoveryUsesProviderInventory(t *testing.T) {
	h := newNativeRuntimeTestHarness(t)
	h.r.APIReader = h.r.Client
	// A tenant-only cache contains neither the provider WorkerPool nor its namespace.
	h.r.Client = fake.NewClientBuilder().WithScheme(h.r.Scheme).Build()
	store := &nativeSubstrateTemplateStore{r: h.r}
	object, err := store.Get(t.Context(), substrateTestTemplateNamespace, substrateTestBaseTemplateName)
	if err != nil {
		t.Fatal(err)
	}
	namespace, _, _ := unstructured.NestedString(object.Object, "spec", "workerPoolRef", "namespace")
	name, _, _ := unstructured.NestedString(object.Object, "spec", "workerPoolRef", "name")
	if namespace != substrateTestWorkerNamespace || name != substrateTestWorkerPoolName {
		t.Fatalf("native infrastructure selected WorkerPool %s/%s outside its provider inventory", namespace, name)
	}
}

func TestNativeSubstrateCompilerRejectsOversizedEnvironmentAndUnresolvedReferences(t *testing.T) {
	r, _ := runtimePoolSubstrateTestReconciler(t, nil, &fakeSubstrateActorControl{})
	for _, unresolved := range []bool{false, true} {
		object := nativeSubstrateTestRender(t, r, "nonce")
		containers, _, _ := unstructured.NestedSlice(object.Object, "spec", "containers")
		container := containers[0].(map[string]any)
		env := make([]any, 0, 33)
		for i := range 33 {
			env = append(env, map[string]any{"name": fmt.Sprintf("VAR_%d", i), "value": "public"})
		}
		if unresolved {
			env = []any{map[string]any{"name": "UNRESOLVED", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.uid"}}}}
		}
		container["env"] = env
		if err := unstructured.SetNestedSlice(object.Object, containers, "spec", "containers"); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeSubstrateRuntimeTemplate(object); err == nil {
			t.Fatal("native compiler admitted an unsupported environment")
		}
	}
}

type nativeTemplateTestAPI struct {
	ateapipb.ControlClient
	templates       map[string]*ateapipb.ActorTemplate
	creates         int
	createCalls     int
	createErr       error
	failAfterCreate bool
}

func (a *nativeTemplateTestAPI) GetActorTemplate(_ context.Context, req *ateapipb.GetActorTemplateRequest, _ ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	key := req.GetActorTemplate().GetAtespace() + "/" + req.GetActorTemplate().GetName()
	if value := a.templates[key]; value != nil {
		return proto.Clone(value).(*ateapipb.ActorTemplate), nil
	}
	return nil, status.Error(codes.NotFound, "template not found")
}

func (a *nativeTemplateTestAPI) CreateActorTemplate(_ context.Context, req *ateapipb.CreateActorTemplateRequest, _ ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	a.createCalls++
	if a.createErr != nil {
		return nil, a.createErr
	}
	value := proto.Clone(req.GetActorTemplate()).(*ateapipb.ActorTemplate)
	key := value.GetMetadata().GetAtespace() + "/" + value.GetMetadata().GetName()
	if a.templates[key] != nil {
		return nil, status.Error(codes.AlreadyExists, "immutable template exists")
	}
	a.creates++
	value.Metadata.Uid = fmt.Sprintf("native-template-%d", a.creates)
	value.Metadata.Version = 1
	a.templates[key] = value
	if a.failAfterCreate {
		a.failAfterCreate = false
		return nil, status.Error(codes.Unavailable, "response lost")
	}
	return proto.Clone(value).(*ateapipb.ActorTemplate), nil
}

func (a *nativeTemplateTestAPI) DeleteActorTemplate(_ context.Context, req *ateapipb.DeleteActorTemplateRequest, _ ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	key := req.GetActorTemplate().GetAtespace() + "/" + req.GetActorTemplate().GetName()
	value := a.templates[key]
	if value == nil {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	delete(a.templates, key)
	return value, nil
}

func TestNativeSubstrateTemplateBindingRecoversLostCreateAndKeepsRevisions(t *testing.T) {
	r, pool := runtimePoolSubstrateTestReconciler(t, nil, &fakeSubstrateActorControl{})
	r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).Build()
	r.ControllerNamespace = "orka-system"
	api := &nativeTemplateTestAPI{templates: map[string]*ateapipb.ActorTemplate{}, failAfterCreate: true}
	r.SubstrateNativeClientFactory = func(SubstrateConfig) (*workspace.SubstrateNativeClient, error) {
		return &workspace.SubstrateNativeClient{Control: api}, nil
	}
	store := &nativeSubstrateTemplateStore{r: r}
	first := nativeSubstrateTestRender(t, r, "nonce-1")
	if err := store.Create(t.Context(), pool, first); err == nil {
		t.Fatal("lost response should be observable")
	}
	if err := store.Create(t.Context(), pool, first); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatal("lost response duplicated native template creation")
	}
	observed, err := store.Get(t.Context(), first.GetNamespace(), first.GetName())
	if err != nil {
		t.Fatal(err)
	}
	second := nativeSubstrateTestRender(t, r, "nonce-2")
	if err := store.Update(t.Context(), observed, second); err != nil {
		t.Fatal(err)
	}
	if len(api.templates) != 2 {
		t.Fatal("template rotation overwrote an immutable native revision")
	}
	observed, err = store.Get(t.Context(), second.GetNamespace(), second.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), observed); err != nil {
		t.Fatal(err)
	}
	if len(api.templates) != 0 {
		t.Fatal("owned template revisions leaked after cleanup")
	}
	var records corev1.ConfigMapList
	if err := r.List(t.Context(), &records); err != nil || len(records.Items) != 0 {
		t.Fatal("template ownership record was not collected")
	}
}

func TestNativeSubstrateTemplateBindingRejectsReplacementAndCleansPendingCreate(t *testing.T) {
	r, pool := runtimePoolSubstrateTestReconciler(t, nil, &fakeSubstrateActorControl{})
	r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orka-system"}}).Build()
	r.ControllerNamespace = "orka-system"
	api := &nativeTemplateTestAPI{templates: map[string]*ateapipb.ActorTemplate{}, failAfterCreate: true}
	r.SubstrateNativeClientFactory = func(SubstrateConfig) (*workspace.SubstrateNativeClient, error) {
		return &workspace.SubstrateNativeClient{Control: api}, nil
	}
	store := &nativeSubstrateTemplateStore{r: r}
	desired := nativeSubstrateTestRender(t, r, "nonce")
	if err := store.Create(t.Context(), pool, desired); err == nil {
		t.Fatal("expected lost create response")
	}
	pending, err := store.GetForCleanup(t.Context(), desired.GetNamespace(), desired.GetName())
	if err != nil || pending == nil {
		t.Fatal("interrupted creation lost durable cleanup intent")
	}
	if err := store.Delete(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	if len(api.templates) != 0 {
		t.Fatal("pending native template leaked")
	}
	if err := store.Create(t.Context(), pool, desired); err != nil {
		t.Fatal(err)
	}
	for _, template := range api.templates {
		template.Metadata.Uid = "foreign-lifetime"
	}
	if _, err := store.Get(t.Context(), desired.GetNamespace(), desired.GetName()); err == nil {
		t.Fatal("native template replacement was adopted")
	}
	owned, err := store.GetForCleanup(t.Context(), desired.GetNamespace(), desired.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), owned); err == nil {
		t.Fatal("foreign native template was deleted")
	}
}
