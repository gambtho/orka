package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type unavailableSubstrateRecoveryReader struct{ client.Reader }

func (unavailableSubstrateRecoveryReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("Kubernetes read unavailable")
}

func TestSubstrateRecoveryDiscoveryRequiresReadableControllerNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "other-controller", Namespace: "elsewhere", Labels: map[string]string{substrateCatalogLabel: substrateOwnedLabelValue},
	}}).Build()
	if state, err := FindSubstrateRecoveryJournal(t.Context(), reader, "controller-system"); err != nil || state != "" {
		t.Fatalf("discovery crossed controller namespace: state=%q err=%v", state, err)
	}
	if _, err := FindSubstrateRecoveryJournal(t.Context(), reader, ""); err == nil {
		t.Fatal("discovery admitted an unscoped controller namespace")
	}
	if _, err := FindSubstrateRecoveryJournal(t.Context(), unavailableSubstrateRecoveryReader{reader}, "controller-system"); err == nil || !strings.Contains(err.Error(), "Kubernetes read unavailable") {
		t.Fatalf("unreadable journal inventory treated as absent: %v", err)
	}
}
