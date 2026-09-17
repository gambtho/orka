/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orka-agents/orka/internal/gateway/protocol"
)

func TestLoadDeliveryFixture(t *testing.T) {
	const valid = `{"accountId":"fixture-account","contextId":"fixture-context",` +
		`"replyTarget":"fixture-reply","originatingEventId":"fixture-event"}`
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"valid", valid, true},
		{"optional thread", strings.TrimSuffix(valid, "}") + `,"threadId":"fixture-thread"}`, true},
		{"exact body limit", valid + strings.Repeat(" ", protocol.MaxHTTPBodyBytes-len(valid)), true},
		{"over body limit", valid + strings.Repeat(" ", protocol.MaxHTTPBodyBytes+1-len(valid)), false},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"private-field":"private-content"}`, false},
		{"caller text", strings.TrimSuffix(valid, "}") + `,"text":"private-content"}`, false},
		{"caller delivery ID", strings.TrimSuffix(valid, "}") + `,"deliveryId":"private-content"}`, false},
		{"caller idempotency ID", strings.TrimSuffix(valid, "}") + `,"idempotencyId":"private-content"}`, false},
		{"caller metadata", strings.TrimSuffix(valid, "}") + `,"metadata":{"private-field":"private-content"}}`, false},
		{"caller credentials", strings.TrimSuffix(valid, "}") + `,"authorizationValue":"private-content"}`, false},
		{"trailing JSON", valid + ` {"private-field":"private-content"}`, false},
		{"trailing malformed", valid + ` private-content`, false},
		{"malformed", `{"private-field":"private-content"`, false},
		{"invalid UTF8", strings.Replace(valid, "fixture-account", "fixture-\xff-account", 1), false},
		{"lone surrogate", strings.Replace(valid, "fixture-context", `room-\ud800`, 1), false},
		{"empty", "", false},
		{"null", "null", false},
		{"array", "[]", false},
		{"wrong type", `{"accountId":123}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-path.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal("could not create test fixture")
			}
			fixture, err := loadDeliveryFixture(path)
			if tc.valid {
				if err != nil || fixture == nil {
					t.Fatal("valid fixture rejected")
				}
				if fixture.AccountID != "fixture-account" || fixture.ContextID != "fixture-context" ||
					fixture.ReplyTarget != "fixture-reply" || fixture.OriginatingEventID != "fixture-event" {
					t.Error("loaded routing fields differ")
				}
				if tc.name == "optional thread" && fixture.ThreadID != "fixture-thread" {
					t.Error("thread missing")
				}
			} else if err == nil || fixture != nil {
				t.Error("invalid fixture accepted")
			} else if err.Error() != "invalid delivery fixture" {
				t.Error("fixture diagnostic is not the fixed sanitized message")
			}
		})
	}
}

func TestLoadDeliveryFixtureReadFailurePrivacy(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "private-missing-path"), t.TempDir()} {
		fixture, err := loadDeliveryFixture(path)
		if fixture != nil || err == nil {
			t.Error("unreadable fixture accepted")
		} else if err.Error() != "could not read delivery fixture" {
			t.Error("read diagnostic is not fixed and sanitized")
		}
	}
}

func TestLoadDeliveryFixtureUnicode(t *testing.T) {
	cases := []struct {
		name, encoded, want string
		valid               bool
	}{
		{"raw UTF8", "fixture-\U0001f600", "fixture-\U0001f600", true},
		{"BMP escape", `\u0066ixture-context`, "fixture-context", true},
		{"surrogate pair", `fixture-\ud83d\ude00`, "fixture-\U0001f600", true},
		{"boundary pairs", `fixture-\ud800\udc00\uDBFF\uDFFF`, "fixture-\U00010000\U0010ffff", true},
		{"replacement character", `fixture-\ufffd`, "fixture-\ufffd", true},
		{"escaped surrogate text", `fixture-\\ud800`, `fixture-\ud800`, true},
		{"lone low surrogate", `fixture-\udc00`, "", false},
		{"surrogate before text", `fixture-\ud800-text`, "", false},
		{"surrogate before BMP", `fixture-\ud800\u0061`, "", false},
		{"two high surrogates", `fixture-\ud800\ud800`, "", false},
		{"reversed pair", `fixture-\udc00\ud800`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"accountId":"fixture-account","contextId":"` + tc.encoded +
				`","replyTarget":"fixture-reply","originatingEventId":"fixture-event"}`
			path := filepath.Join(t.TempDir(), "private-path.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal("could not create test fixture")
			}
			fixture, err := loadDeliveryFixture(path)
			if tc.valid {
				if err != nil || fixture == nil {
					t.Fatal("valid Unicode fixture rejected")
				}
				if fixture.ContextID != tc.want {
					t.Error("loaded Unicode routing identity differs")
				}
			} else if err == nil || fixture != nil {
				t.Error("malformed Unicode fixture accepted")
			} else if err.Error() != "invalid delivery fixture" {
				t.Error("fixture diagnostic is not the fixed sanitized message")
			}
		})
	}
}
