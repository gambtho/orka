/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package conformance

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/orka-agents/orka/internal/gateway/protocol"
)

var fixturePercentEscape = regexp.MustCompile(`%[0-9a-fA-F]{2}`)

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
	// V1 validates trimmed required identities and does not bound the optional thread.
	// Check the original values because those are what the fixture transmits.
	for _, identity := range []string{f.AccountID, f.ContextID, f.ThreadID, f.ReplyTarget, f.OriginatingEventID} {
		if len(identity) > protocol.MaxIdentityBytes || !utf8.ValidString(identity) || strings.ContainsFunc(identity, unicode.IsControl) {
			return protocol.DeliveryRequest{}, errors.New("invalid delivery fixture")
		}
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
		decoded := value
		for range 8 {
			for _, identity := range identities {
				identity = strings.TrimSpace(identity)
				if identity == "" {
					continue
				}
				// HTTP and JSON errors can embed the identity inside a larger quoted field.
				quoted := strconv.Quote(identity)
				if strings.Contains(decoded, identity) || strings.Contains(decoded, quoted[1:len(quoted)-1]) {
					return redactedValue
				}
			}
			// Decode a copy for matching URL paths, including nested escapes. Decode
			// valid escapes individually so unrelated literal percent signs cannot
			// prevent masking. Keep safe output and transmitted routing unchanged.
			next := fixturePercentEscape.ReplaceAllStringFunc(decoded, func(escape string) string {
				unescaped, _ := url.PathUnescape(escape)
				return unescaped
			})
			if next == decoded {
				return value
			}
			decoded = next
		}
		// Bound work on deeply nested input without exposing an unchecked field.
		return redactedValue
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
