/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package conformance

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/orka-agents/orka/internal/gateway/protocol"
)

// DeliveryFixture supplies only the routing identities for a conformance delivery.
type DeliveryFixture struct {
	AccountID          string `json:"accountId"`
	ContextID          string `json:"contextId"`
	ThreadID           string `json:"threadId,omitempty"`
	ReplyTarget        string `json:"replyTarget"`
	OriginatingEventID string `json:"originatingEventId"`
}

func (f DeliveryFixture) deliveryRequest() (protocol.DeliveryRequest, error) {
	runID, err := uuid.NewRandom()
	if err != nil {
		return protocol.DeliveryRequest{}, errors.New("could not initialize delivery fixture")
	}
	id := "conformance-" + runID.String() + "-auth"
	delivery := protocol.DeliveryRequest{
		ProtocolVersion: protocol.Version, DeliveryID: id, IdempotencyID: id,
		Kind: protocol.DeliveryKindFinal, Text: "[Orka conformance check] No action required.",
		AccountID: f.AccountID, ContextID: f.ContextID, ThreadID: f.ThreadID,
		ReplyTarget: f.ReplyTarget, OriginatingEvent: f.OriginatingEventID,
	}
	if err := protocol.ValidateDeliveryRequest(&delivery); err != nil {
		return protocol.DeliveryRequest{}, errors.New("invalid delivery fixture")
	}
	// V1's outbound validator does not bound the optional thread identity.
	if len(f.ThreadID) > protocol.MaxIdentityBytes || !utf8.ValidString(f.ThreadID) || strings.ContainsFunc(f.ThreadID, unicode.IsControl) {
		return protocol.DeliveryRequest{}, errors.New("invalid delivery fixture")
	}
	return delivery, nil
}

func (f DeliveryFixture) maskResultFields(
	message string, capabilities *protocol.CapabilitiesResponse,
) (string, *protocol.CapabilitiesResponse) {
	identities := []string{f.AccountID, f.ContextID, f.ThreadID, f.ReplyTarget, f.OriginatingEventID}
	// Mask whole fields before bearer sanitization: partial replacements can hide
	// overlapping identities and expose their remaining fragments.
	mask := func(value string) string {
		for _, identity := range identities {
			if identity = strings.TrimSpace(identity); identity != "" && strings.Contains(value, identity) {
				return redactedValue
			}
		}
		return value
	}
	message = mask(message)
	if capabilities == nil {
		return message, nil
	}
	result := *capabilities
	result.ProtocolVersion = mask(result.ProtocolVersion)
	result.AdapterName = mask(result.AdapterName)
	result.AdapterVersion = mask(result.AdapterVersion)
	return message, &result
}
