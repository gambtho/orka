package gateway

import (
	"testing"

	gatewayv1alpha1 "github.com/orka-agents/orka/api/gateway/v1alpha1"
	"github.com/orka-agents/orka/internal/gateway/protocol"
	"github.com/orka-agents/orka/internal/store"
	"github.com/stretchr/testify/require"
)

func TestGatewayMeetingDraftCanonicalPersistenceReplay(t *testing.T) {
	for _, subject := range []string{" Planning ", "token is unit-test-placeholder"} {
		t.Run(subject, func(t *testing.T) {
			s, db, adapter, event, task := newGatewayMessageFixture(t)
			s.Config.InterimMessagesPerTask = 1
			updateMessageGateway(t, s, func(g *gatewayv1alpha1.Gateway) { g.Status.ObservedCapabilities.Capabilities.MeetingDrafts = true })
			draft := protocol.MeetingDraft{Subject: subject, Agenda: []string{" Next steps "}, Attendees: protocol.MeetingAttendees{Mode: "named", Names: []string{" Alice Smith "}}, PreferredWindow: " Tomorrow "}
			enqueue := func() *TaskMessageReceipt {
				prepared, err := s.PrepareTaskMeetingDraft(t.Context(), task.Namespace, task.Name, string(task.UID), "meeting", "Draft above", draft)
				require.NoError(t, err)
				receipt, err := s.EnqueuePreparedTaskMessage(t.Context(), prepared)
				require.NoError(t, err)
				return receipt
			}
			first := enqueue()
			replay := enqueue()
			require.Equal(t, first.DeliveryID, replay.DeliveryID)
			require.False(t, replay.Created)
			row, err := db.GetGatewayDelivery(t.Context(), event.Namespace, first.DeliveryID)
			require.NoError(t, err)
			want := "Planning"
			if subject != " Planning " {
				want = "token is [REDACTED]"
			}
			require.Equal(t, want, row.Metadata["meeting.subject"])
			require.Equal(t, "Next steps", row.Metadata["meeting.agenda.0"])
			require.Equal(t, "Alice Smith", row.Metadata["meeting.name.0"])
			require.Equal(t, "Tomorrow", row.Metadata["meeting.preferredWindow"])
			require.NoError(t, s.DeliverOnce(t.Context()))
			require.Len(t, adapter.Deliveries(), 1)
			require.Equal(t, row.Metadata, adapter.Deliveries()[0].Metadata)
			require.NotContains(t, adapter.Deliveries()[0].Metadata["meeting.subject"], "unit-test-placeholder")
			replay = enqueue()
			require.False(t, replay.Created)
			rows, err := db.ListGatewayDeliveries(t.Context(), store.GatewayDeliveryFilter{Namespace: event.Namespace})
			require.NoError(t, err)
			require.Len(t, rows, 1)
		})
	}
}

func TestGatewayMeetingDraftCapabilityWithdrawalAbandons(t *testing.T) {
	s, db, adapter, event, task := newGatewayMessageFixture(t)
	draft := protocol.MeetingDraft{Subject: "Planning", Agenda: []string{"Next steps"}, Attendees: protocol.MeetingAttendees{Mode: "everyone"}}
	denied, err := s.PrepareTaskMeetingDraft(t.Context(), task.Namespace, task.Name, string(task.UID), "meeting", "Draft above", draft)
	require.NoError(t, err)
	_, err = s.EnqueuePreparedTaskMessage(t.Context(), denied)
	require.ErrorIs(t, err, ErrMeetingDraftsUnsupported)
	updateMessageGateway(t, s, func(g *gatewayv1alpha1.Gateway) { g.Status.ObservedCapabilities.Capabilities.MeetingDrafts = true })
	prepared, err := s.PrepareTaskMeetingDraft(t.Context(), task.Namespace, task.Name, string(task.UID), "meeting", "Draft above", draft)
	require.NoError(t, err)
	receipt, err := s.EnqueuePreparedTaskMessage(t.Context(), prepared)
	require.NoError(t, err)
	_, err = s.EnqueueTaskMessage(t.Context(), task.Namespace, task.Name, string(task.UID), "text", "ordinary progress")
	require.NoError(t, err)
	updateMessageGateway(t, s, func(g *gatewayv1alpha1.Gateway) { g.Status.ObservedCapabilities.Capabilities.MeetingDrafts = false })
	require.NoError(t, s.DeliverOnce(t.Context()))
	row, err := db.GetGatewayDelivery(t.Context(), event.Namespace, receipt.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, store.GatewayDeliveryDeadLettered, row.State)
	require.Empty(t, adapter.Deliveries())
	require.NoError(t, s.DeliverOnce(t.Context()))
	require.Len(t, adapter.Deliveries(), 1)
	require.Equal(t, "ordinary progress", adapter.Deliveries()[0].Text)
	prepared, err = s.PrepareTaskMeetingDraft(t.Context(), task.Namespace, task.Name, string(task.UID), "meeting", "Draft above", draft)
	require.NoError(t, err)
	replay, err := s.EnqueuePreparedTaskMessage(t.Context(), prepared)
	require.NoError(t, err)
	require.Equal(t, receipt.DeliveryID, replay.DeliveryID)
	require.False(t, replay.Created)
}
