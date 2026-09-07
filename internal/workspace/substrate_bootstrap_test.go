package workspace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
	"github.com/stretchr/testify/require"
)

func TestNativeSubstrateWorkspaceSealedBootstrap(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact actor", true: "replaced actor"}[mismatch], func(t *testing.T) {
			const secret = "fixture-signing-secret"
			key, err := harnessv2.WorkspaceBootstrapPublicKey(secret)
			require.NoError(t, err)
			identity := harnessv2.SubstrateActorIdentity{Atespace: "ate-demo", Name: "direct", UID: "test-actor-uid"}
			if mismatch {
				identity.UID = "replacement-actor-uid"
			}
			receiver, err := harnessv2.NewCredentialBootstrapReceiver(harnessv2.WorkspaceBootstrapNonce(key), identity)
			require.NoError(t, err)
			puts := 0
			router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "direct.ate-demo.actors.test", r.Host)
				require.Equal(t, harnessv2.CredentialBootstrapPath, r.URL.Path)
				require.Empty(t, r.Header.Get("Authorization"))
				if r.Method == http.MethodGet {
					require.NoError(t, json.NewEncoder(w).Encode(receiver.Challenge))
					return
				}
				puts++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NotContains(t, string(body), secret)
				require.NotContains(t, string(body), "fixture-handoff")
				require.NoError(t, harnessv2.VerifyCredentialBootstrap(key, receiver.Challenge.Nonce, body, r.Header.Get(harnessv2.CredentialBootstrapSignatureHeader)))
				var envelope harnessv2.SealedCredentialBootstrap
				require.NoError(t, json.Unmarshal(body, &envelope))
				plain, err := receiver.Open(envelope)
				require.NoError(t, err)
				var request harnessv2.WorkspaceBootstrapRequest
				require.NoError(t, json.Unmarshal(plain, &request))
				require.Equal(t, "fixture-handoff", request.HandoffToken)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer router.Close()
			executor, err := NewSubstrateExecutor(SubstrateConfig{RouterURL: router.URL, ActorDNSSuffix: "actors.test", BootstrapToken: secret, SealedBootstrap: true,
				ControlClient: &recordingSubstrateControlClient{getStatuses: []string{substrateStatusRunning, substrateStatusRunning}}})
			require.NoError(t, err)
			_, err = executor.Upload(t.Context(), UploadRequest{Ref: WorkspaceRef{ID: "direct.ate-demo"}, BootstrapHandoff: true, Timeout: time.Second,
				Artifacts: []UploadArtifact{{Path: substrateHandoffTokenUploadPath, Data: []byte("fixture-handoff")}}})
			if mismatch {
				require.Error(t, err)
				require.Zero(t, puts, "a replacement Actor must not receive a credential")
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, puts)
			}
		})
	}
}

func TestNativeSubstrateWorkspaceRefusesPlaintextFallback(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasPrefix(r.URL.Path, "/v2/"))
		w.WriteHeader(http.StatusNotFound)
	}))
	defer router.Close()
	executor, err := NewSubstrateExecutor(SubstrateConfig{RouterURL: router.URL, ActorDNSSuffix: "actors.test", BootstrapToken: "fixture-secret", SealedBootstrap: true,
		ControlClient: &recordingSubstrateControlClient{getStatuses: []string{substrateStatusRunning}}})
	require.NoError(t, err)
	_, err = executor.Upload(t.Context(), UploadRequest{Ref: WorkspaceRef{ID: "direct.ate-demo"}, BootstrapHandoff: true, Timeout: time.Second,
		Artifacts: []UploadArtifact{{Path: substrateHandoffTokenUploadPath, Data: []byte("fixture-handoff")}}})
	require.Error(t, err)
}

func TestNativeSubstrateWorkspaceBootstrapRefusesRedirect(t *testing.T) {
	followed := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer destination.Close()
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer router.Close()
	executor, err := NewSubstrateExecutor(SubstrateConfig{
		RouterURL: router.URL, ActorDNSSuffix: "actors.test", BootstrapToken: "fixture-secret", SealedBootstrap: true,
		ControlClient: &recordingSubstrateControlClient{getStatuses: []string{substrateStatusRunning}},
	})
	require.NoError(t, err)
	_, err = executor.Upload(t.Context(), UploadRequest{
		Ref: WorkspaceRef{ID: "direct.ate-demo"}, BootstrapHandoff: true, Timeout: time.Second,
		Artifacts: []UploadArtifact{{Path: substrateHandoffTokenUploadPath, Data: []byte("fixture-handoff")}},
	})
	require.Error(t, err)
	require.False(t, followed, "the provider route must remain the bootstrap destination")
}
