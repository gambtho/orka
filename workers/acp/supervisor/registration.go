package supervisor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	apiyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
)

type registrationMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// The input deliberately excludes generated claims and stored-object metadata.
type registrationTemplate struct {
	metav1.TypeMeta `json:",inline"`
	Metadata        registrationMetadata `json:"metadata"`
	Spec            struct {
		ContractVersion corev1alpha1.AgentRuntimeContractVersion `json:"contractVersion"`
		Deployment      corev1alpha1.AgentRuntimeDeploymentSpec  `json:"deployment"`
		ClientAuth      corev1alpha1.AgentRuntimeClientAuth      `json:"clientAuth"`
		Capabilities    struct {
			Profile struct {
				ProviderKind    string                       `json:"providerKind"`
				WorkspaceIntent corev1alpha1.WorkspaceIntent `json:"workspaceIntent"`
			} `json:"profile"`
			MCPPolicy *corev1alpha1.AgentRuntimeMCPPolicySpec `json:"mcpPolicy"`
		} `json:"capabilities"`
	} `json:"spec"`
}

// ExportRegistrationFromEnv expands an AgentKit or Foundry registration template
// using the same non-secret profile and capability inputs as supervisor startup.
// It does not read credentials, start a supervisor, contact a service, or allocate
// session identities. An explicit instance ID is required because another
// process cannot reconstruct a running supervisor's generated boot identity.
func ExportRegistrationFromEnv(input io.Reader, output io.Writer) error {
	template, err := readRegistrationTemplate(input)
	if err != nil {
		return err
	}
	profile, provider, err := runtimeProfileFromEnv()
	if err != nil {
		return err
	}
	if profile.ProviderKind != providerKindAgentKit && profile.ProviderKind != providerKindFoundry {
		return fmt.Errorf("registration export supports AgentKit and Foundry external profiles")
	}
	if template.Spec.Capabilities.Profile.ProviderKind != profile.ProviderKind ||
		template.Spec.Capabilities.Profile.WorkspaceIntent != corev1alpha1.WorkspaceIntent(profile.WorkspaceIntent) {
		return fmt.Errorf("template provider or workspace intent does not match the configured supervisor")
	}
	instance := requiredEnv(EnvRuntimeInstanceID)
	if instance == "" {
		return fmt.Errorf("registration export requires an explicit %s", EnvRuntimeInstanceID)
	}
	if _, err := harnessv2.PathSegment("runtime instance ID", instance); err != nil {
		return fmt.Errorf("invalid %s", EnvRuntimeInstanceID)
	}
	if err := validateRegistrationPolicy(template.Spec.Capabilities.MCPPolicy, profile); err != nil {
		return err
	}
	capabilities, err := capabilitiesForProfile(profile)
	if err != nil {
		return err
	}
	if err := capabilities.Validate(); err != nil {
		return fmt.Errorf("configured capabilities: %w", err)
	}
	claims := registrationCapabilities(instance, profile, provider, capabilities)
	claims.MCPPolicy = template.Spec.Capabilities.MCPPolicy
	manifest := struct {
		metav1.TypeMeta `json:",inline"`
		Metadata        registrationMetadata                  `json:"metadata"`
		Spec            corev1alpha1.AgentRuntimeRegistrySpec `json:"spec"`
	}{
		TypeMeta: template.TypeMeta, Metadata: template.Metadata,
		Spec: corev1alpha1.AgentRuntimeRegistrySpec{
			ContractVersion: &template.Spec.ContractVersion,
			Deployment:      template.Spec.Deployment, ClientAuth: template.Spec.ClientAuth,
			Capabilities: claims,
		},
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode registration: %w", err)
	}
	_, err = output.Write(encoded)
	return err
}

func readRegistrationTemplate(input io.Reader) (registrationTemplate, error) {
	var template registrationTemplate
	const maxTemplateBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(input, maxTemplateBytes+1))
	if err != nil || len(data) > maxTemplateBytes {
		return template, fmt.Errorf("registration template must be readable and at most 64 KiB")
	}
	reader := apiyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	document, err := reader.Read()
	if err != nil || yaml.UnmarshalStrict(document, &template) != nil {
		return template, fmt.Errorf("invalid registration template YAML or unknown fields")
	}
	if _, err := reader.Read(); err != io.EOF {
		return template, fmt.Errorf("registration template must contain exactly one document")
	}
	return template, template.validate()
}

func (t registrationTemplate) validate() error {
	if t.APIVersion != corev1alpha1.GroupVersion.String() || t.Kind != "AgentRuntime" ||
		t.Spec.ContractVersion != corev1alpha1.AgentRuntimeContractHarnessV2 {
		return fmt.Errorf("template must be a core.orka.ai/v1alpha1 AgentRuntime using orka.harness.v2")
	}
	if len(validation.IsDNS1123Subdomain(t.Metadata.Name)) != 0 ||
		(t.Metadata.Namespace != "" && len(validation.IsDNS1123Label(t.Metadata.Namespace)) != 0) {
		return fmt.Errorf("template requires a valid registration name and optional namespace")
	}
	endpoint := t.Spec.Deployment.Endpoint
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != providerProxyScheme && parsed.Scheme != providerProxyTLSScheme) ||
		parsed.User != nil || strings.ContainsAny(endpoint, "?# \t\r\n") ||
		t.Spec.Deployment.Mode != corev1alpha1.AgentRuntimeDeploymentModeExternalEndpoint ||
		t.Spec.Deployment.KubernetesRecovery != nil {
		return fmt.Errorf("template requires an external endpoint without credentials, query, fragment, or recovery ownership")
	}
	if _, err := harnessv2.NewClient(endpoint); err != nil {
		return fmt.Errorf("template endpoint must be a canonical harness v2 URL")
	}
	auth := t.Spec.ClientAuth
	if auth.BearerAuthRef != nil {
		return fmt.Errorf("template must use v2 authentication references")
	}
	for _, ref := range []*corev1alpha1.AgentRuntimeSecretKeyReference{auth.ControllerBearerTokenSecretRef, auth.OperationCapabilitySecretRef} {
		if ref == nil || len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || len(validation.IsConfigMapKey(ref.Key)) != 0 {
			return fmt.Errorf("template requires both v2 authentication Secret names and keys")
		}
	}
	if *auth.ControllerBearerTokenSecretRef == *auth.OperationCapabilitySecretRef {
		return fmt.Errorf("controller bearer token and operation capability must use distinct Secret keys")
	}
	return nil
}

func validateRegistrationPolicy(policy *corev1alpha1.AgentRuntimeMCPPolicySpec, profile harnessv2.RuntimeProfile) error {
	if policy == nil || policy.AllowedTools == nil || policy.DisallowedTools == nil || policy.ApprovalRequiredTools == nil {
		return fmt.Errorf("template mcpPolicy requires explicit allowedTools, disallowedTools, and approvalRequiredTools lists")
	}
	if len(policy.ApprovalRequiredTools) != 0 {
		return fmt.Errorf("external runtime profiles do not support approval-required tools")
	}
	for _, names := range [][]string{policy.AllowedTools, policy.DisallowedTools} {
		if len(names) > 128 {
			return fmt.Errorf("MCP policy lists must contain at most 128 tools")
		}
		for i, name := range names {
			if strings.TrimSpace(name) != name || len(name) == 0 || len(name) > 128 || (i > 0 && names[i-1] >= name) {
				return fmt.Errorf("MCP policy lists must contain sorted, unique, nonempty tool names of at most 128 bytes")
			}
		}
	}
	toolDigest, err := harnessv2.CanonicalRuntimeToolPolicyDigest(policy.AllowedTools, policy.DisallowedTools, policy.AllowBash)
	if err != nil || toolDigest != profile.ToolPolicyDigest {
		return fmt.Errorf("template tool policy does not match the configured supervisor digest")
	}
	// The controller canonicalizes an empty approval list to a nil slice.
	approvalDigest, err := harnessv2.CanonicalMCPApprovalPolicyDigest(harnessv2.MCPApprovalPolicy{})
	if err != nil || approvalDigest != profile.ApprovalPolicyDigest {
		return fmt.Errorf("template approval policy does not match the configured supervisor digest")
	}
	mcpDigest, err := harnessv2.CanonicalMCPConfigurationDigest(policy.AllowedTools)
	if err != nil || mcpDigest != profile.MCPConfigurationDigest {
		return fmt.Errorf("template MCP configuration does not match the configured supervisor digest")
	}
	return nil
}

func registrationCapabilities(
	instance string,
	profile harnessv2.RuntimeProfile,
	provider ProviderProfile,
	caps harnessv2.CapabilitiesResponse,
) *corev1alpha1.AgentRuntimeCapabilitiesSpec {
	registeredProfile := &corev1alpha1.AgentRuntimeProfileSpec{
		Digest: string(caps.RuntimeProfileDigest), DigestSchemaVersion: int32(caps.ProfileDigestSchemaVersion),
		ACPProfile: profile.ACPProfile, AdapterName: provider.AdapterName, AdapterDigest: provider.AdapterDigest,
		ProviderKind: profile.ProviderKind, Model: profile.Model,
		AgentConfigurationDigest: profile.AgentConfigurationDigest, ToolPolicyDigest: profile.ToolPolicyDigest,
		ApprovalPolicyDigest: profile.ApprovalPolicyDigest, MCPConfigurationDigest: profile.MCPConfigurationDigest,
		WorkspaceIntent:     corev1alpha1.WorkspaceIntent(profile.WorkspaceIntent),
		ProxyCredentialRole: profile.ProxyCredentialRole, ProxyCredentialScope: profile.ProxyCredentialScope,
		ResourceClass: profile.ResourceClass,
	}
	if profile.ModelLimits != nil {
		registeredProfile.ModelLimits = &corev1alpha1.ModelTokenLimits{Context: profile.ModelLimits.Context, Output: profile.ModelLimits.Output}
	}
	l, g := caps.Limits, caps.WorkspaceGovernance
	return &corev1alpha1.AgentRuntimeCapabilitiesSpec{
		RuntimeInstanceID: instance, Profile: registeredProfile,
		Limits: &corev1alpha1.AgentRuntimeProtocolLimits{
			MaxResidentSessions: int32(l.MaxResidentSessions), MaxConcurrentPrompts: int32(l.MaxConcurrentPrompts),
			MaxRequestBytes: int32(l.MaxRequestBytes), MaxEventLineBytes: int32(l.MaxEventLineBytes),
			MaxTerminalResultBytes: int32(l.MaxTerminalResultBytes), MaxBufferedEvents: int32(l.MaxBufferedEvents),
			MaxUpdateEventsPerSecond: int32(l.MaxUpdateEventsPerSecond),
			MinPromptLeaseMillis:     l.MinPromptLeaseMillis, MaxPromptLeaseMillis: l.MaxPromptLeaseMillis,
			MaxPendingPermissions: int32(l.MaxPendingPermissions), MaxWorkspaceDeltaBytes: l.MaxWorkspaceDeltaBytes,
		},
		SupportsDrain: caps.SupportsDrain, SupportsPublicationFinalization: caps.SupportsPublicationFinalization,
		WorkspaceGovernance: &corev1alpha1.AgentRuntimeWorkspaceGovernanceCapabilities{
			Mode: corev1alpha1.AgentRuntimeWorkspaceGovernanceMode(g.Mode), Trusted: g.Trusted,
			OrkaOwnedWorkspaceDeltas: g.OrkaOwnedWorkspaceDeltas, PromptScopedBrokerAuthorization: g.PromptScopedBrokerAuthorization,
			NoDirectSCMPublication: g.NoDirectSCMPublication, OrkaOwnedCleanRoomPublication: g.OrkaOwnedCleanRoomPublication,
			ExactInstanceFencing: g.ExactInstanceFencing, DuplicateSafeMutations: g.DuplicateSafeMutations,
			CancellationSettlement: g.CancellationSettlement,
		},
	}
}
