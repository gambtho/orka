package supervisor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/orka-agents/orka/internal/acp"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
)

// Exact structural envelopes from the pinned codex-acp 1.1.7 bundle's
// createMcpToolCallUpdate and buildPermissionRequest. The permission callback
// must retain the structured identity from the same prompt's preceding update.
func TestCodexMCPPermissionUsesPinnedStructuredIdentity(t *testing.T) {
	server, cfg, _ := newTestServer(t, "immediate")
	fence := cfg.Fence
	fence.RuntimeSessionUID = "permission-session"
	fence.RuntimeSessionGeneration = 1
	state := &sessionState{
		descriptor:  harnessv2.RuntimeSessionDescriptor{RuntimeSessionUID: fence.RuntimeSessionUID, Generation: 1},
		profile:     harnessv2.RuntimeProfile{ProviderKind: providerKindCodex},
		permissions: make(map[harnessv2.PermissionRequestID]permissionState),
		mcpProxy: &mcpProxySession{configuration: harnessv2.MCPPolicyConfiguration{ToolPolicy: harnessv2.MCPToolPolicy{
			AllowedToolNames: []string{"reply_in_conversation"},
			Tools:            []harnessv2.MCPToolDescriptor{{Name: "reply_in_conversation", Source: harnessv2.MCPToolSourceBrokeredBuiltin}},
		}}},
	}
	prompt := &promptState{request: testStartPromptRequest(t, cfg, fence)}
	_, err := server.mapRuntimeEvent(state, prompt, acp.PromptEvent{
		Type: acp.PromptEventUpdate, Timestamp: time.Now().UTC(),
		Update: &acp.SessionNotification{Update: json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","kind":"execute","title":"mcp.orka.reply_in_conversation","status":"in_progress","rawInput":{"server":"orka","tool":"reply_in_conversation","arguments":{"content":"synthetic"}},"_meta":{"is_mcp_tool_call":true}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := server.mapRuntimeEvent(state, prompt, acp.PromptEvent{
		Type: acp.PromptEventPermissionRequested, Timestamp: time.Now().UTC(),
		Permission: &acp.PermissionRequestEvent{RequestID: "permission-1", Request: acp.RequestPermissionRequest{
			ToolCall: json.RawMessage(`{"toolCallId":"call-1","kind":"execute","status":"pending"}`),
			Meta:     acp.Meta{"is_mcp_tool_approval": true},
			Options:  []acp.PermissionOption{{OptionID: "accept", Name: "Allow once", Kind: "allow_once"}, {OptionID: "decline", Name: "Decline", Kind: "reject_once"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mapped.PermissionRequested.ToolName != "reply_in_conversation" {
		t.Fatalf("pinned Codex MCP permission identity = %q, want reply_in_conversation; empty identity causes frozen policy to reject before broker dispatch", mapped.PermissionRequested.ToolName)
	}
}

func TestCodexMCPPermissionIdentityFailsClosed(t *testing.T) {
	const reply = "reply_in_conversation"
	policy := harnessv2.MCPToolPolicy{
		AllowedToolNames: []string{"Read", reply, "run_validation"},
		Tools: []harnessv2.MCPToolDescriptor{
			{Name: "Read", Source: harnessv2.MCPToolSourceProviderNative},
			{Name: reply, Source: harnessv2.MCPToolSourceBrokeredBuiltin},
			{Name: "run_validation", Source: harnessv2.MCPToolSourceBrokeredCustom},
		},
	}
	for _, test := range []struct {
		name     string
		provider string
		update   string
		deny     bool
		want     string
		wantErr  bool
	}{
		{name: "registered builtin", update: `{"rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`, want: reply},
		{name: "registered custom", update: `{"rawInput":{"server":"orka","tool":"run_validation"},"_meta":{"is_mcp_tool_call":true}}`, want: "run_validation"},
		{name: "matching explicit identity", update: `{"name":"reply_in_conversation","rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`, want: reply},
		{name: "display title alone", update: `{"title":"mcp.orka.reply_in_conversation"}`},
		{name: "unmarked raw input", update: `{"rawInput":{"server":"orka","tool":"reply_in_conversation"}}`},
		{name: "false marker", update: `{"rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":false}}`},
		{name: "wrong server", update: `{"rawInput":{"server":"other","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "native name collision", update: `{"rawInput":{"server":"orka","tool":"Read"},"_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "unregistered tool", update: `{"rawInput":{"server":"orka","tool":"unknown"},"_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "explicit denial", update: `{"rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`, deny: true, wantErr: true},
		{name: "missing raw input", update: `{"title":"mcp.orka.reply_in_conversation","_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "wrong raw input type", update: `{"rawInput":"reply_in_conversation","_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "wrong marker type", update: `{"_meta":{"is_mcp_tool_call":"true"}}`, wantErr: true},
		{name: "conflicting explicit identity", update: `{"name":"Read","rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`, wantErr: true},
		{name: "Claude cannot use Codex envelope", provider: providerKindClaude, update: `{"rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`},
		{name: "other provider metadata remains opaque", provider: providerKindClaude, update: `{"_meta":{"is_mcp_tool_call":"provider-extension"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := test.provider
			if provider == "" {
				provider = providerKindCodex
			}
			var update map[string]any
			if err := json.Unmarshal([]byte(test.update), &update); err != nil {
				t.Fatal(err)
			}
			update["sessionUpdate"] = "tool_call"
			update["toolCallId"] = "call-1"
			data, err := json.Marshal(update)
			if err != nil {
				t.Fatal(err)
			}
			frozen := policy
			if test.deny {
				frozen.DisallowedToolNames = []string{reply}
			}
			prompt := &promptState{}
			err = prompt.rememberToolCallName(&acp.SessionNotification{Update: data}, provider, frozen)
			if (err != nil) != test.wantErr {
				t.Fatalf("remember identity error = %v, want error %v", err, test.wantErr)
			}
			id, err := canonicalACPToolCallID("call-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := prompt.toolCallNames[id]; got != test.want {
				t.Fatalf("remembered tool = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCodexMCPPermissionIdentityCannotCrossCallsOrPrompts(t *testing.T) {
	server, cfg, _ := newTestServer(t, "immediate")
	fence := cfg.Fence
	fence.RuntimeSessionUID = "permission-session"
	fence.RuntimeSessionGeneration = 1
	policy := harnessv2.MCPToolPolicy{
		AllowedToolNames: []string{"reply_in_conversation", "run_validation"},
		Tools: []harnessv2.MCPToolDescriptor{
			{Name: "reply_in_conversation", Source: harnessv2.MCPToolSourceBrokeredBuiltin},
			{Name: "run_validation", Source: harnessv2.MCPToolSourceBrokeredCustom},
		},
	}
	original := &promptState{request: testStartPromptRequest(t, cfg, fence)}
	update := &acp.SessionNotification{Update: json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","rawInput":{"server":"orka","tool":"reply_in_conversation"},"_meta":{"is_mcp_tool_call":true}}`)}
	if err := original.rememberToolCallName(update, providerKindCodex, policy); err != nil {
		t.Fatal(err)
	}
	changed := &acp.SessionNotification{Update: json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","rawInput":{"server":"orka","tool":"run_validation"},"_meta":{"is_mcp_tool_call":true}}`)}
	if err := original.rememberToolCallName(changed, providerKindCodex, policy); err == nil {
		t.Fatal("same call ID changed its structured tool identity")
	}
	for _, test := range []struct {
		name   string
		prompt *promptState
		call   string
	}{
		{name: "different call", prompt: original, call: "call-2"},
		{name: "new prompt", prompt: &promptState{request: original.request}, call: "call-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &sessionState{
				descriptor: harnessv2.RuntimeSessionDescriptor{RuntimeSessionUID: fence.RuntimeSessionUID, Generation: 1},
				profile:    harnessv2.RuntimeProfile{ProviderKind: providerKindCodex}, permissions: make(map[harnessv2.PermissionRequestID]permissionState),
				mcpProxy: &mcpProxySession{configuration: harnessv2.MCPPolicyConfiguration{ToolPolicy: policy}},
			}
			call, err := json.Marshal(map[string]string{"toolCallId": test.call, "kind": "execute", "title": "mcp.orka.reply_in_conversation"})
			if err != nil {
				t.Fatal(err)
			}
			mapped, err := server.mapRuntimeEvent(state, test.prompt, acp.PromptEvent{
				Type: acp.PromptEventPermissionRequested, Timestamp: time.Now().UTC(),
				Permission: &acp.PermissionRequestEvent{RequestID: "permission-1", Request: acp.RequestPermissionRequest{
					ToolCall: call, Options: []acp.PermissionOption{{OptionID: "allow", Name: "Allow once", Kind: "allow_once"}},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if mapped.PermissionRequested.ToolName != "" {
				t.Fatal("permission borrowed identity from another call or prompt")
			}
		})
	}
}
