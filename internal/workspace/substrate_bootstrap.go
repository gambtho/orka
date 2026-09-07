package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
)

func isSubstrateHandoffUpload(path string) bool {
	clean := filepath.Clean(path)
	return clean == substrateHandoffTokenUploadPath || clean == "/app/"+substrateHandoffTokenUploadPath || clean == "/workspace/"+substrateHandoffTokenUploadPath
}

func (e *SubstrateWorkspaceExecutor) seedNativeWorkspaceCredential(ctx context.Context, actorID, token string) error {
	secret, err := e.requireBootstrapToken("native bootstrap")
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("native workspace handoff credential is empty")
	}
	actor, err := e.control.GetActor(ctx, actorID)
	if err != nil {
		return err
	}
	if actor == nil || actor.ActorUID == "" || actor.Status != substrateStatusRunning {
		return fmt.Errorf("native bootstrap requires an exact running Actor")
	}
	if (e.sessionIdentity != nil || e.sessionIdentityToken != "") && e.cacheSessionIdentityHandoff(actorID, actor, "") != token {
		return fmt.Errorf("native workspace credential belongs to another Actor or Pod lifetime")
	}
	ref, err := substrateObjectRef(actorID, actor.Atespace)
	if err != nil {
		return err
	}
	identity := harnessv2.SubstrateActorIdentity{Atespace: ref.Atespace, Name: ref.Name, UID: actor.ActorUID}
	key, err := harnessv2.WorkspaceBootstrapPublicKey(secret)
	if err != nil {
		return err
	}
	nonce := harnessv2.WorkspaceBootstrapNonce(key)
	url := e.routerURL + harnessv2.CredentialBootstrapPath
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid native bootstrap route")
	}
	get.Host = SubstrateActorKey(ref.Atespace, ref.Name) + "." + e.actorDNSSuffix
	client := *e.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(get)
	if err != nil {
		return fmt.Errorf("native workspace bootstrap challenge is unavailable")
	}
	var challenge harnessv2.SealedBootstrapChallenge
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&challenge)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil {
		return fmt.Errorf("native workspace did not provide a sealed bootstrap challenge")
	}
	// The provider projects Actor identity but not Pod identity into the
	// process. A read after the challenge binds its process key to the observed
	// assignment; later rerouting cannot decrypt this process's envelope.
	observed, err := e.control.GetActor(ctx, actorID)
	if err != nil {
		return err
	}
	if observed == nil || observed.ActorUID != actor.ActorUID || observed.ActorVersion != actor.ActorVersion ||
		observed.PodUID != actor.PodUID || observed.Status != substrateStatusRunning {
		return fmt.Errorf("native Actor lifetime changed before credential bootstrap")
	}
	payload, err := json.Marshal(harnessv2.WorkspaceBootstrapRequest{HandoffToken: token})
	if err != nil {
		return err
	}
	body, err := harnessv2.SealCredentialBootstrap(challenge, nonce, identity, payload)
	if err != nil {
		return err
	}
	signature, err := harnessv2.SignCredentialBootstrap(harnessv2.WorkspaceBootstrapSigningSeed(secret), nonce, body)
	if err != nil {
		return err
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid native bootstrap route")
	}
	put.Host = get.Host
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set(harnessv2.CredentialBootstrapNonceHeader, nonce)
	put.Header.Set(harnessv2.CredentialBootstrapSignatureHeader, signature)
	response, err = client.Do(put)
	if err != nil {
		return fmt.Errorf("native workspace bootstrap result is unconfirmed")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("native workspace rejected sealed bootstrap with HTTP %d", response.StatusCode)
	}
	observed, err = e.control.GetActor(ctx, actorID)
	if err != nil {
		return err
	}
	if observed == nil || observed.ActorUID != actor.ActorUID || observed.PodUID != actor.PodUID || observed.Status != substrateStatusRunning {
		return fmt.Errorf("native Actor lifetime changed during credential bootstrap")
	}
	return nil
}
