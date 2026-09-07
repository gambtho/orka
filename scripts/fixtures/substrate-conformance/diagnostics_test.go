package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/orka-agents/orka/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestCommandFailurePreservesDiagnosticsWithoutCredentials(t *testing.T) {
	cfg := workspace.SubstrateConfig{HandoffToken: "fixture-handoff-value", BootstrapToken: "fixture-bootstrap-value"}
	result := &workspace.ExecResult{
		ExitCode: 1, Stdout: "never-print-command-output", Stderr: "permission denied\n" + cfg.BootstrapToken,
	}
	err := workspace.NewError("exec", workspace.ErrorKindCommandFailed, "command failed", false,
		errors.New("rejected "+cfg.HandoffToken+" Authorization: Bearer fixture-header-value"))
	message := commandFailure("native command", cfg, result, err).Error()
	require.Contains(t, message, "kind=CommandFailed")
	require.Contains(t, message, "exit=1")
	require.Contains(t, message, `permission denied\n`)
	for _, value := range []string{cfg.HandoffToken, cfg.BootstrapToken, result.Stdout, "fixture-header-value"} {
		require.NotContains(t, message, value)
	}
	result.Stderr = strings.Repeat("x", maxCommandFailureBytes-70) + cfg.BootstrapToken + strings.Repeat("x", 100)
	message = commandFailure("native command", cfg, result, nil).Error()
	require.LessOrEqual(t, len(message), maxCommandFailureBytes+len("native command:  (truncated)"))
	require.Contains(t, message, "(truncated)")
	require.NotContains(t, message, "fixture-")
	require.Contains(t, commandFailure("native command", cfg, nil, err).Error(), "result=nil")
}
