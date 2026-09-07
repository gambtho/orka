package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
	"github.com/stretchr/testify/require"
)

func TestNativeSubstrateAgentBootstrapRejectsReplayAndCredentialReplacement(t *testing.T) {
	const secret = "fixture-signing-secret"
	t.Setenv(envHandoffAuth, "")
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	tokenPath := filepath.Join(directory, "handoff")
	previousRoots := allowedRoots
	allowedRoots = []string{directory}
	t.Cleanup(func() { allowedRoots = previousRoots })
	t.Setenv(envHandoffAuthFile, tokenPath)
	key, err := harnessv2.WorkspaceBootstrapPublicKey(secret)
	require.NoError(t, err)
	nonce := harnessv2.WorkspaceBootstrapNonce(key)
	identity := harnessv2.SubstrateActorIdentity{Atespace: "test", Name: "direct", UID: "actor-uid"}
	receiver, err := harnessv2.NewCredentialBootstrapReceiver(nonce, identity)
	require.NoError(t, err)
	server := newWorkspaceAgentServer()
	server.bootstrapPublicKey, server.bootstrapReceiver = key, receiver
	seal := func(token string) []byte {
		plain, err := json.Marshal(harnessv2.WorkspaceBootstrapRequest{HandoffToken: token})
		require.NoError(t, err)
		body, err := harnessv2.SealCredentialBootstrap(receiver.Challenge, nonce, identity, plain)
		require.NoError(t, err)
		return body
	}
	put := func(body []byte, signingSecret string) int {
		signature, err := harnessv2.SignCredentialBootstrap(
			harnessv2.WorkspaceBootstrapSigningSeed(signingSecret), nonce, body,
		)
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPut, harnessv2.CredentialBootstrapPath, bytes.NewReader(body))
		request.Header.Set(harnessv2.CredentialBootstrapNonceHeader, nonce)
		request.Header.Set(harnessv2.CredentialBootstrapSignatureHeader, signature)
		response := httptest.NewRecorder()
		server.routes().ServeHTTP(response, request)
		return response.Code
	}
	body := seal("fixture-handoff")
	require.Equal(t, http.StatusForbidden, put(body, "wrong-signing-secret"))
	require.NoFileExists(t, tokenPath)
	require.Equal(t, http.StatusNoContent, put(body, secret))
	require.Equal(t, http.StatusNoContent, put(body, secret), "the same process may confirm a lost response")
	require.Equal(t, http.StatusConflict, put(seal("different-handoff"), secret))
	data, err := os.ReadFile(tokenPath)
	require.NoError(t, err)
	require.Equal(t, "fixture-handoff", string(data))
	info, err := os.Stat(tokenPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	server.bootstrapReceiver, err = harnessv2.NewCredentialBootstrapReceiver(nonce, identity)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, put(body, secret), "a replacement process cannot open the previous envelope")
}
