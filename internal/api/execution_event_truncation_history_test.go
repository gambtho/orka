package api

import (
	"context"
	"strings"
	"testing"

	"github.com/orka-agents/orka/internal/events"
	harnessv2 "github.com/orka-agents/orka/internal/harness/v2"
)

func TestExecutionEventTruncationKeepsBoundarySeparator(t *testing.T) {
	for _, test := range []struct {
		name, prefix, padding string
		limit                 int
		contentText           bool
	}{
		{name: "transcript-summary-ascii", padding: "x", limit: 4096},
		{name: "transcript-summary-unicode", padding: "界", limit: 4096},
		{name: "transcript-content", padding: "界", limit: 32768, contentText: true},
		{name: "tool-output-summary", padding: "界", limit: 4096},
		{name: "diagnostic-message", prefix: "notice: ", padding: "x", limit: 4096},
		{name: "failed-message", prefix: "notice: ", padding: "x", limit: 4096},
		{name: "plan-summary", prefix: "Plan in progress (0/1 complete): ", padding: "x", limit: 4096},
	} {
		for _, beforeEllipsis := range []bool{false, true} {
			name := test.name + "/pw-at-limit"
			if beforeEllipsis {
				name = test.name + "/pw-before-ellipsis"
			}
			t.Run(name, func(t *testing.T) {
				journal, state := newModelHistoryJournal(t, "")
				paddingRunes := test.limit - len([]rune(test.prefix)) - 2
				wantSuffix := "p…"
				if beforeEllipsis {
					paddingRunes--
					wantSuffix = "pw…"
				}
				padding := strings.Repeat(test.padding, paddingRunes)
				value := padding + "pw harmless suffix."
				wantFirst := test.prefix + padding + wantSuffix
				event := modelHistoryEvent(1)
				appendEvent := state.AppendUpdateIfNew
				event.Type = harnessv2.EventUpdate
				switch test.name {
				case "transcript-summary-ascii", "transcript-summary-unicode", "transcript-content":
					event.Type = harnessv2.EventCompleted
					event.Completed = &harnessv2.CompletedEvent{
						StopReason: harnessv2.ACPStopReasonEndTurn,
						Result:     harnessv2.PromptResult{Content: []harnessv2.ContentBlock{{Type: harnessv2.ContentBlockText, Text: value}}},
					}
				case "tool-output-summary":
					event = markerWhitespaceToolEvent(1, "", value)
				case "diagnostic-message":
					event.Update = &harnessv2.UpdateEvent{Kind: harnessv2.UpdateDiagnostic,
						Diagnostic: &harnessv2.DiagnosticUpdate{Code: "notice", Message: value}}
				case "failed-message":
					event.Type = harnessv2.EventFailed
					event.Failed = &harnessv2.FailedEvent{Code: "notice", Message: value}
					appendEvent = state.AppendPromptLifecycleIfNew
				case "plan-summary":
					event.Update = &harnessv2.UpdateEvent{Kind: harnessv2.UpdatePlan,
						Plan: &harnessv2.PlanUpdate{Entries: []harnessv2.PlanEntry{{Content: value, Status: harnessv2.PlanEntryInProgress}}}}
				}
				if err := event.Validate(harnessv2.DefaultEventStreamLimits()); err != nil {
					t.Fatal(err)
				}
				for _, wantNew := range []bool{true, false} {
					var isNew bool
					var err error
					if strings.HasPrefix(test.name, "transcript-") {
						_, isNew, err = state.AppendAssistantTranscriptIfNew(context.Background(), event, value, false)
					} else {
						_, isNew, err = appendEvent(context.Background(), event)
					}
					if err != nil || isNew != wantNew {
						t.Fatalf("append source: new=%t want=%t err=%v", isNew, wantNew, err)
					}
				}
				const later = "d=fixture-value"
				appendMarkerWhitespaceUpdate(t, state, markerWhitespaceToolEvent(2, "Inspect", later), true)
				rows := markerWhitespaceRows(t, journal, 2)
				first := markerWhitespacePublicDTO(t, rows[0])
				published := first.Summary
				if test.contentText {
					published = first.ContentText
				}
				if published != wantFirst {
					runes := []rune(published)
					t.Fatalf("public cutoff: %d runes, suffix %q; want suffix %q", len(runes), string(runes[max(0, len(runes)-4):]), wantSuffix)
				}
				second := markerWhitespacePublicDTO(t, rows[1])
				if second.ContentText != later {
					t.Errorf("safe later public output = %q, want %q", second.ContentText, later)
				}
				// Join the exact strings returned by SQLite and the API DTO. The
				// ellipsis must not be stripped to manufacture a pwd assignment.
				joined := published + second.ContentText
				if events.RedactExecutionEventText(joined) != joined {
					t.Error("actual public cutoff and later output reconstruct a protected assignment")
				}
				if events.RedactExecutionEventText("pw"+later) == "pw"+later {
					t.Fatal("synthetic fixture must be sensitive without the truncation separator")
				}
			})
		}
	}
}
