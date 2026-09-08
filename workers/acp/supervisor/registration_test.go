package supervisor

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
	"sigs.k8s.io/yaml"
)

func TestExportRegistrationMatchesSupervisorConfig(t *testing.T) {
	for _, backend := range []string{providerKindAgentKit, providerKindFoundry} {
		for _, withModelLimits := range []bool{false, true} {
			name := backend
			if withModelLimits {
				name += "/model-limits"
			}
			t.Run(name, func(t *testing.T) {
				setArtifactClientSupervisorEnv(t)
				input := setRegistrationEnv(t, backend)
				if withModelLimits {
					t.Setenv(EnvModelContextLimit, "128000")
					t.Setenv(EnvModelOutputLimit, "16384")
				}
				cfg, err := LoadConfigFromEnv()
				if err != nil {
					t.Fatal(err)
				}
				// Export must not read auth files, consume bootstrap credentials,
				// or need any of the mutable process/session configuration.
				for _, key := range []string{EnvControllerTokenFile, EnvCapabilitySecretFile, EnvProviderTokenFile} {
					t.Setenv(key, filepath.Join(t.TempDir(), "does-not-exist"))
				}
				for _, key := range []string{EnvControllerEpoch, EnvRuntimePoolUID, EnvRuntimePoolGeneration, EnvMCPBrokerURL, EnvSessionBaseDir} {
					t.Setenv(key, "")
				}
				const marker = "fixture-bootstrap-material-must-stay-private"
				for _, key := range []string{EnvControllerTokenBootstrap, EnvCapabilitySecretBootstrap, EnvProviderTokenBootstrap} {
					t.Setenv(key, marker)
				}
				var output bytes.Buffer
				if err := ExportRegistrationFromEnv(strings.NewReader(input), &output); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(output.String(), marker) || os.Getenv(EnvControllerTokenBootstrap) != marker ||
					os.Getenv(EnvCapabilitySecretBootstrap) != marker || os.Getenv(EnvProviderTokenBootstrap) != marker {
					t.Fatal("export disclosed or consumed bootstrap credentials")
				}
				var registration corev1alpha1.AgentRuntime
				if err := yaml.UnmarshalStrict(output.Bytes(), &registration); err != nil {
					t.Fatal(err)
				}
				claims := registration.Spec.Capabilities
				if claims.RuntimeInstanceID != string(cfg.Fence.RuntimeInstanceID) || claims.Profile.Digest != string(cfg.Capabilities.RuntimeProfileDigest) ||
					claims.Profile.AdapterName != cfg.Provider.AdapterName || claims.Profile.AdapterDigest != cfg.Provider.AdapterDigest ||
					claims.SupportsDrain != cfg.Capabilities.SupportsDrain || claims.SupportsPublicationFinalization != cfg.Capabilities.SupportsPublicationFinalization {
					t.Fatal("export does not match supervisor identity or capability claims")
				}
				if (claims.Profile.ModelLimits != nil) != withModelLimits {
					t.Fatal("export lost model limits")
				}
				var limits harnessv2.ProtocolLimits
				var governance harnessv2.WorkspaceGovernanceCapabilities
				for _, pair := range []struct{ source, target any }{{claims.Limits, &limits}, {claims.WorkspaceGovernance, &governance}} {
					encoded, err := json.Marshal(pair.source)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(encoded, pair.target); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(limits, cfg.Capabilities.Limits) || !reflect.DeepEqual(governance, cfg.Capabilities.WorkspaceGovernance) {
					t.Fatal("export does not match advertised limits or governance guarantees")
				}
				if strings.Contains(output.String(), "supportsAgentSessionConfiguration") || strings.Contains(output.String(), "status:") {
					t.Fatal("export contains HTTP-only capabilities or status")
				}
			})
		}
	}
}

func TestExportRegistrationRejectsMismatchWithoutOutput(t *testing.T) {
	tests := []struct {
		name, env, value, before, after, want string
	}{
		{name: "unknown boot identity", env: EnvRuntimeInstanceID, want: "explicit ORKA_ACP_RUNTIME_INSTANCE_ID"},
		{name: "invalid instance", env: EnvRuntimeInstanceID, value: "runtime/other", want: "invalid ORKA_ACP_RUNTIME_INSTANCE_ID"},
		{name: "wrong backend", env: EnvProvider, value: providerKindFoundry, want: "template provider"},
		{name: "wrong intent", env: EnvWorkspaceIntent, value: "write", want: "workspace intent"},
		{name: "built-in profile", env: EnvProvider, value: providerKindCodex, want: "external profiles"},
		{name: "invalid adapter digest", env: EnvAgentKitAdapterDigest, value: "invalid", want: "sha256 digest"},
		{name: "missing configuration digest", env: EnvAgentConfigurationDigest, want: "runtime profile"},
		{name: "tool policy drift", env: EnvToolPolicyDigest, value: testAgentKitAdapterDigest, want: "tool policy"},
		{name: "approval policy drift", env: EnvApprovalPolicyDigest, value: testAgentKitAdapterDigest, want: "approval policy"},
		{name: "MCP configuration drift", env: EnvMCPConfigurationDigest, value: testAgentKitAdapterDigest, want: "MCP configuration"},
		{name: "missing policy list", before: "      allowedTools: []\n", want: "explicit allowedTools"},
		{name: "approval tools", before: "approvalRequiredTools: []", after: "approvalRequiredTools: [lookup]", want: "approval-required"},
		{name: "unsorted tools", before: "allowedTools: []", after: "allowedTools: [second, first]", want: "sorted, unique"},
		{name: "generated field override", before: "providerKind: agentkit", after: "providerKind: agentkit\n      digest: ignored", want: "unknown fields"},
		{name: "unknown field", before: "allowBash: false", after: "allowBahs: false", want: "unknown fields"},
		{name: "duplicate field", before: "allowBash: false", after: "allowBash: false\n      allowBash: true", want: "invalid registration template"},
		{name: "wrong contract", before: "orka.harness.v2", after: "orka.harness.v1", want: "using orka.harness.v2"},
		{name: "missing auth reference", before: "      key: token\n", want: "Secret names and keys"},
		{name: "escaped path", before: ":8080\n", after: ":8080/%2Fadmin\n", want: "canonical harness v2 URL"},
		{name: "backslash path", before: ":8080\n", after: ":8080/prefix\\othere\n", want: "canonical harness v2 URL"},
		{name: "noncanonical path", before: ":8080\n", after: ":8080/prefix/../other\n", want: "canonical harness v2 URL"},
		{name: "reused auth key", before: "fibey-agentkit-operation-auth\n      key: capability-secret", after: "fibey-agentkit-controller-auth\n      key: token", want: "distinct Secret keys"},
		{name: "second document", before: "", after: "\n---\nkind: Secret\n", want: "exactly one document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := setRegistrationEnv(t, providerKindAgentKit)
			if tt.env != "" {
				t.Setenv(tt.env, tt.value)
			} else if tt.before == "" {
				input += tt.after
			} else {
				if !strings.Contains(input, tt.before) {
					t.Fatal("template no longer contains the field under test")
				}
				input = strings.Replace(input, tt.before, tt.after, 1)
			}
			var output bytes.Buffer
			err := ExportRegistrationFromEnv(strings.NewReader(input), &output)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("export error = %v, want %q", err, tt.want)
			}
			if output.Len() != 0 {
				t.Fatal("failed export emitted a manifest")
			}
		})
	}
}

func TestExportRegistrationRejectsEndpointCredentials(t *testing.T) {
	input := setRegistrationEnv(t, providerKindAgentKit)
	password := rand.Text()
	endpoint := url.URL{
		Scheme: providerProxyScheme, Host: "fibey-agentkit-runtime.orka-system.svc.cluster.local:8080",
		User: url.UserPassword("fixture-user", password),
	}
	input = strings.Replace(input, "http://fibey-agentkit-runtime.orka-system.svc.cluster.local:8080", endpoint.String(), 1)
	var output bytes.Buffer
	err := ExportRegistrationFromEnv(strings.NewReader(input), &output)
	if err == nil || !strings.Contains(err.Error(), "without credentials") {
		t.Fatal("credential-bearing endpoint was not rejected")
	}
	if output.Len() != 0 || strings.Contains(err.Error(), password) {
		t.Fatal("failed export emitted a manifest or sensitive input")
	}
}

func setRegistrationEnv(t *testing.T, backend string) string {
	t.Helper()
	toolDigest, err := harnessv2.CanonicalRuntimeToolPolicyDigest([]string{}, []string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	approvalDigest, err := harnessv2.CanonicalMCPApprovalPolicyDigest(harnessv2.MCPApprovalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	mcpDigest, err := harnessv2.CanonicalMCPConfigurationDigest([]string{})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		EnvProvider: backend, EnvModel: "gpt-test", EnvWorkspaceIntent: "read",
		EnvModelContextLimit: "", EnvModelOutputLimit: "", EnvRuntimeInstanceID: "fixture-instance",
		EnvAgentKitAdapterDigest: testAgentKitAdapterDigest, EnvFoundryAdapterDigest: testAgentKitAdapterDigest,
		EnvAgentConfigurationDigest: testDigest("baked-config"), EnvToolPolicyDigest: toolDigest,
		EnvApprovalPolicyDigest: approvalDigest, EnvMCPConfigurationDigest: mcpDigest,
		EnvProxyCredentialRole: "operator-managed", EnvProxyCredentialScope: "external-runtime", EnvResourceClass: "external",
	} {
		t.Setenv(key, value)
	}
	path := filepath.Join("..", "..", "..", "examples", "fibey-custom-agent-demo", "agentruntime-"+backend+".yaml")
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}
