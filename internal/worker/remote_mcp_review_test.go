package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orka-agents/orka/internal/contexttoken"
)

func TestRemoteMCPSSEOptionalFieldSpace(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		valid       bool
	}{
		{"space", "event: message", true},
		{"no space", "event:message", true},
		{"extra space", "event:  message", false},
		{"different event", "event: sampling", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readRemoteMCPEvent(strings.NewReader(tc.event + "\ndata:{}\n\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("SSE event acceptance = %t; want %t", err == nil, tc.valid)
			}
		})
	}
}

func TestRemoteMCPSSETerminalResponseBoundary(t *testing.T) {
	response := "data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"ok\":true}}\n\n"
	interaction := "data: {\"jsonrpc\":\"2.0\",\"id\":\"interaction\",\"method\":\"sampling/createMessage\",\"params\":{}}\n\n"
	for _, mode := range []string{"open after response", "interaction after response", "interaction before response"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "interaction before response" {
					_, _ = w.Write([]byte(interaction))
				}
				_, _ = w.Write([]byte(response))
				if mode == "interaction after response" {
					_, _ = w.Write([]byte(interaction))
				}
				if mode == "open after response" {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = remoteMCPExchange(server.Client(), req, "1")
			if (err != nil) != (mode == "interaction before response") {
				t.Fatal("SSE did not stop at the validated terminal response or reject a preceding interaction")
			}
		})
	}
}

func TestRemoteMCPResponseIDsCompareExactStringValues(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"plain", `"1"`, true},
		{"escaped", `"\u0031"`, true},
		{"number", `1`, false},
		{"null", `null`, false},
		{"boolean", `true`, false},
		{"object", `{}`, false},
		{"array", `["1"]`, false},
		{"different", `"2"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + tc.id + `,"result":{"ok":true}}`))
			}))
			defer server.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = remoteMCPExchange(server.Client(), req, "1")
			if (err == nil) != tc.valid {
				t.Fatalf("response identity acceptance = %t; want %t", err == nil, tc.valid)
			}
		})
	}
}

func TestRemoteMCPRedactsBoundAndExchangedTransactionTokens(t *testing.T) {
	for _, exchange := range []bool{false, true} {
		name := "bound"
		if exchange {
			name = "exchanged"
		}
		t.Run(name, func(t *testing.T) {
			f := &remoteProtocolFixture{result: `{"content":[{"type":"text","text":"test-task-authority transaction-token test-resource-credential"}],"structuredContent":{"test-task-authority":{"nested":["test-task-authority","transaction-token","test-resource-credential"]}}}`}
			e := remoteTestExecutor(t, f.serve(t))
			if exchange {
				e.SetTransactionExchangeConfig(&TransactionExchangeConfig{
					TTS:       contexttoken.TTSConfig{Endpoint: "https://issuer.example.test/token", TokenSource: contexttoken.TTSTokenSourceIncoming},
					Exchanger: fakeContextTokenExchanger{},
				})
			}
			result, err := e.Execute(t.Context(), remoteTestTool(t), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal("remote execution failed")
			}
			secrets := []string{"test-task-authority", "test-resource-credential"}
			if exchange {
				secrets = append(secrets, "transaction-token")
			}
			for _, secret := range secrets {
				if strings.Contains(result, secret) {
					t.Fatal("remote result exposed a bound credential")
				}
			}
		})
	}
}
