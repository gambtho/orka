/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/orka-agents/orka/internal/gateway/conformance"
	"github.com/orka-agents/orka/internal/gateway/protocol"
)

func main() {
	endpoint := flag.String("endpoint", "", "adapter base URL")
	tokenEnv := flag.String(
		"token-env", "ORKA_GATEWAY_BEARER_TOKEN",
		"environment variable containing the outbound bearer token",
	)
	timeout := flag.Duration("timeout", 15*time.Second, "per-request timeout")
	referenceFixtures := flag.Bool("reference-fixtures", false, "run optional reference-adapter fault fixtures")
	deliveryFixturePath := flag.String(
		"delivery-fixture", "", "JSON file containing delivery routing identities (sends a real test message)",
	)
	flag.Parse()
	if strings.TrimSpace(*endpoint) == "" {
		fmt.Fprintln(os.Stderr, "--endpoint is required")
		os.Exit(2)
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if token == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", *tokenEnv)
		os.Exit(2)
	}
	var deliveryFixture *conformance.DeliveryFixture
	if *deliveryFixturePath != "" {
		if *referenceFixtures {
			fmt.Fprintln(os.Stderr, "delivery fixture cannot be combined with reference fixtures")
			os.Exit(2)
		}
		var err error
		deliveryFixture, err = loadDeliveryFixture(*deliveryFixturePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4**timeout)
	defer cancel()
	result := conformance.Check(ctx, conformance.Target{
		BaseURL: *endpoint, AuthorizationValue: token, Timeout: *timeout, ReferenceFixtures: *referenceFixtures,
		DeliveryFixture: deliveryFixture,
	})
	_ = writeResult(os.Stdout, result, token)
	if !result.Passed {
		os.Exit(1)
	}
}

func loadDeliveryFixture(path string) (*conformance.DeliveryFixture, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not read delivery fixture")
	}
	defer file.Close() //nolint:errcheck
	body, err := io.ReadAll(io.LimitReader(file, protocol.MaxHTTPBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read delivery fixture")
	}
	if len(body) > protocol.MaxHTTPBodyBytes {
		return nil, fmt.Errorf("invalid delivery fixture")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var fixture *conformance.DeliveryFixture
	if err := decoder.Decode(&fixture); err != nil || fixture == nil {
		return nil, fmt.Errorf("invalid delivery fixture")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid delivery fixture")
	}
	return fixture, nil
}

func writeResult(writer io.Writer, result conformance.CheckResult, token string) error {
	return json.NewEncoder(writer).Encode(conformance.SanitizeCheckResult(result, token))
}
