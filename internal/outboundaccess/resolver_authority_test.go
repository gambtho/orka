package outboundaccess

import (
	"context"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type credentialReadRecorder struct {
	client.Reader
	reads int
}

func (r *credentialReadRecorder) Get(
	ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption,
) error {
	if _, secret := object.(*corev1.Secret); secret {
		r.reads++
	}
	return r.Reader.Get(ctx, key, object, options...)
}

func TestGatewayCredentialAuthorityPrecedesReferenceSecretReads(t *testing.T) {
	for _, scopeAllowed := range []bool{false, true} {
		name := "denied scope"
		if scopeAllowed {
			name = "different Secret"
		}
		t.Run(name, func(t *testing.T) {
			policy := readyPolicy("gateway", corev1alpha1.OutboundAccessPolicySpec{Gateway: &corev1alpha1.GatewayOutboundAccess{
				ServiceRef: serviceRef("gateway", "", 8443), Scheme: "https",
				TLS: &corev1alpha1.OutboundTLSConfig{CASecretRef: secretRef("gateway-ca", "ca.crt")},
			}})
			service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "tenant"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8443}}}}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gateway-ca", Namespace: "tenant"}, Data: map[string][]byte{"ca.crt": []byte("must-not-read")}}
			reader := &credentialReadRecorder{Reader: fake.NewClientBuilder().WithScheme(resolverScheme(t)).WithObjects(policy, service, secret).Build()}
			resolver := &KubernetesResolver{Reader: reader}
			_, err := resolver.Resolve(t.Context(), ResolveRequest{
				Namespace: "tenant", PolicyName: policy.Name, CredentialAuthorityEnforced: true,
				CredentialScopeAllowed: scopeAllowed, CredentialSecret: "different-secret",
			})
			if err == nil {
				t.Fatal("unavailable credential authority accepted")
			}
			if reader.reads != 0 {
				t.Fatalf("reference resolution read %d Secrets before Task credential authorization", reader.reads)
			}
		})
	}
}
