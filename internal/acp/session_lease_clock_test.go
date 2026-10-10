package acp

import (
	"bufio"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// A relative timer may have elapsed while a backward wall-clock step leaves
// the controller's wire deadline in the future. Shortening only the timer
// models that mismatch without mutating a global clock or the absolute bound.
func TestRuntimeSessionLeaseEarlyCallbackRearmsAbsoluteExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, peer := newLeaseTestRuntimeSession(t)
		deadline := time.Now().UTC().Add(2 * time.Second)
		run, err := session.StartPromptWithLeaseDeadline(t.Context(), "clock-step", "sha256:clock-step", []ContentBlock{Text("wait")}, deadline)
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(peer)
		request, err := readTestMessage(reader)
		if err != nil {
			t.Fatal(err)
		}
		<-run.Events
		cancelled := make(chan time.Time, 1)
		peerDone := make(chan error, 1)
		go func() {
			message, err := readTestMessage(reader)
			if err != nil {
				peerDone <- err
				return
			}
			if message.Method != MethodSessionCancel {
				peerDone <- errors.New("expected session/cancel")
				return
			}
			cancelled <- time.Now().UTC()
			peerDone <- writeTestMessage(peer, map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"stopReason": StopReasonCancelled},
			})
		}()
		session.mu.Lock()
		active := session.active
		active.lease.Stop()
		session.mu.Unlock()
		callback := leaseTestExpiryCallback(session, active)
		session.mu.Lock()
		active.lease = time.AfterFunc(time.Second, callback)
		session.mu.Unlock()
		time.Sleep(2*time.Second - time.Nanosecond)
		synctest.Wait()
		select {
		case at := <-cancelled:
			t.Fatalf("early timer cancelled at %v before absolute expiry %v", at, deadline)
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if at := <-cancelled; !at.Equal(deadline) {
			t.Fatalf("rearmed cancellation at %v, want exact expiry %v", at, deadline)
		}
		if err := <-peerDone; err != nil {
			t.Fatal(err)
		}
		if result := <-run.Result; result.Outcome != PromptOutcomeCancelled || !result.SettledAt.Equal(deadline) {
			t.Fatalf("rearmed result = %#v, want cancellation at absolute expiry", result)
		}
	})
}

func TestRuntimeSessionLeaseQueuedCallbackCannotCancelRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, active := newLeaseTestActivePrompt(time.Now().UTC().Add(2 * time.Second))
		session.config.CancelGrace = time.Second
		callback := leaseTestExpiryCallback(session, active)
		release := make(chan struct{})
		active.lease = time.AfterFunc(0, func() { <-release; callback() })
		synctest.Wait() // The relative timer fired, but its callback is queued.
		renewed := time.Now().UTC().Add(3 * time.Second)
		if err := session.RenewPromptLeaseUntil(active.id, renewed); err != nil {
			session.finishPrompt(active, PromptResult{Outcome: PromptOutcomeCompleted})
			close(release)
			synctest.Wait()
			t.Fatalf("still-live absolute authority was not renewable: %v", err)
		}
		defer active.lease.Stop()
		close(release)
		synctest.Wait()
		session.mu.Lock()
		defer session.mu.Unlock()
		if active.cancelRequested || !active.leaseDeadline.Equal(renewed) {
			t.Fatal("queued old callback cancelled renewed authority")
		}
	})
}

func TestRuntimeSessionLeaseStaleCallbackCannotCancelReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, old := newLeaseTestActivePrompt(time.Now().UTC().Add(time.Second))
		callback := leaseTestExpiryCallback(session, old)
		session.finishPrompt(old, PromptResult{Outcome: PromptOutcomeCompleted})
		_, replacement := newLeaseTestActivePrompt(time.Now().UTC().Add(2 * time.Second))
		session.active = replacement // Even identical IDs cannot revive an old callback.
		callbackDone := make(chan struct{})
		go func() { callback(); close(callbackDone) }()
		synctest.Wait()
		cancelled := replacement.cancelRequested
		session.finishPrompt(replacement, PromptResult{Outcome: PromptOutcomeCompleted})
		<-callbackDone
		if cancelled {
			t.Fatal("old prompt callback cancelled replacement authority")
		}
	})
}

func TestRuntimeSessionLeaseCancellationDecisionFencesRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, peer := newLeaseTestRuntimeSession(t)
		deadline := time.Now().UTC().Add(2 * time.Second)
		run, err := session.StartPromptWithLeaseDeadline(t.Context(), "cancel-decision", "sha256:cancel-decision", []ContentBlock{Text("wait")}, deadline)
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(peer)
		request, err := readTestMessage(reader)
		if err != nil {
			t.Fatal(err)
		}
		<-run.Events
		cancelDone := make(chan struct{})
		go func() {
			_, _ = session.CancelPrompt(t.Context(), "cancel-decision")
			close(cancelDone)
		}()
		synctest.Wait() // Cancellation is decided; courtesy cancel is blocked on the pipe.
		err = session.RenewPromptLeaseUntil("cancel-decision", deadline.Add(time.Second))
		if _, ok := errors.AsType[*StalePromptError](err); !ok {
			t.Errorf("renewal after cancellation decision = %v, want stale authority", err)
		}
		message, err := readTestMessage(reader)
		if err != nil || message.Method != MethodSessionCancel {
			t.Fatalf("courtesy cancel = %#v, error = %v", message, err)
		}
		if err := writeTestMessage(peer, map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"result": map[string]any{"stopReason": StopReasonCancelled},
		}); err != nil {
			t.Fatal(err)
		}
		<-run.Result
		<-cancelDone
	})
}

func TestRuntimeSessionLeaseFiredTimerCannotRenewExpiredAuthority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deadline := time.Now().UTC().Add(time.Second)
		session, active := newLeaseTestActivePrompt(deadline)
		release := make(chan struct{})
		active.lease = time.AfterFunc(time.Second, func() { <-release })
		defer close(release)
		time.Sleep(time.Second)
		synctest.Wait()
		err := session.RenewPromptLeaseUntil(active.id, deadline.Add(time.Minute))
		if _, ok := errors.AsType[*StalePromptError](err); !ok || !active.leaseDeadline.Equal(deadline) {
			t.Fatalf("expired fired-timer renewal = %v, deadline = %v", err, active.leaseDeadline)
		}
	})
}

// Captures the callback's authority when the timer is armed.
func leaseTestExpiryCallback(session *RuntimeSession, active *activePrompt) func() {
	version := active.leaseVersion
	return func() { session.expirePrompt(active, version) }
}
