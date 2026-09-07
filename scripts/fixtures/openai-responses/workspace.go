package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
)

const (
	workspaceCanaryFile    = ".orka-checkpoint-canary"
	workspaceCanaryData    = "orka-acp-checkpoint-before-export"
	workspaceCanaryChange  = "orka-acp-checkpoint-after-export"
	workspaceWriteMarker   = "ORKA_NATIVE_FILE_WRITE_OK"
	workspaceResumeMarker  = "ORKA_NATIVE_CONTINUE_OK"
	workspaceRestoreMarker = "ORKA_NATIVE_FORK_OK"
	functionCallType       = "function_call"
	functionCallOutputType = "function_call_output"
	responseCompleted      = "completed"
	responseModelField     = "model"
	responseField          = "response"
	responseItemField      = "item"
)

// These maps hold only fixed scenario keys and fixture-generated call IDs.
// A response marker alone never proves a file operation succeeded.
var workspaceCanaryCalls sync.Map
var workspaceCanaryResults sync.Map

func workspaceCanaryCommand(marker string) string {
	read := "cat " + workspaceCanaryFile
	switch marker {
	case workspaceWriteMarker:
		return "set -eu; umask 077; test ! -e " + workspaceCanaryFile +
			"; printf '%s\\n' '" + workspaceCanaryData + "' > " + workspaceCanaryFile + "; " + read
	case workspaceResumeMarker:
		return "set -eu; " + read + "; printf '%s\\n' '" + workspaceCanaryChange + "' > " + workspaceCanaryFile
	case workspaceRestoreMarker:
		return read
	default:
		return ""
	}
}

// The fixture emits one real Codex shell call, then requires its successful
// output to contain exactly the saved bytes before returning the final marker.
// Read and restore commands contain no copy of the expected file contents.
func handleWorkspaceCanary(
	w http.ResponseWriter, request responsesRequest, body []byte, marker, responseID string,
) bool {
	command := workspaceCanaryCommand(marker)
	if command == "" {
		return false
	}
	key := markerKey(marker)
	if output, found := currentWorkspaceCanaryOutput(body); found {
		callID, issued := workspaceCanaryCalls.Load(key)
		if !issued || output["call_id"] != callID || !validWorkspaceCanaryOutput(output["output"]) {
			http.Error(w, "workspace canary shell result did not match the saved file", http.StatusUnprocessableEntity)
			return true
		}
		workspaceCanaryResults.Store(key, true)
		return false
	}
	tool, namespace := workspaceCanaryTool(body)
	arguments := map[string]any{}
	switch tool {
	case "exec_command":
		arguments["cmd"] = command
		arguments["yield_time_ms"] = 10000
		arguments["max_output_tokens"] = 1024
	case "shell_command":
		arguments["command"] = command
		arguments["timeout_ms"] = 10000
	default:
		http.Error(w, "workspace canary requires an advertised Codex shell tool", http.StatusBadRequest)
		return true
	}
	encoded, _ := json.Marshal(arguments)
	callID := "call_" + responseID
	workspaceCanaryCalls.Store(key, callID)
	item := map[string]any{
		"id": "fc_" + responseID, responseTypeField: functionCallType, responseStatusField: responseCompleted,
		"call_id": callID, "name": tool, "arguments": string(encoded),
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	writeWorkspaceCanaryCall(w, request, responseID, item)
	return true
}

func currentWorkspaceCanaryOutput(body []byte) (map[string]any, bool) {
	items, _ := structuredInputItems(body)
	// Only a tool result after the active user prompt can satisfy this turn.
	// Replayed transcripts and previous turns are never file evidence.
	for _, item := range slices.Backward(items) {
		if item["role"] == messageRoleUser {
			break
		}
		if item["type"] == functionCallOutputType {
			return item, true
		}
	}
	return nil, false
}

func validWorkspaceCanaryOutput(value any) bool {
	parts := appendTextContent(nil, value)
	if len(parts) != 1 {
		return false
	}
	header, output, found := strings.Cut(parts[0], "\nOutput:\n")
	if !found || output != workspaceCanaryData+"\n" {
		return false
	}
	// These are the pinned Codex exec_command and shell_command result formats.
	lines := strings.Split(header, "\n")
	return slices.Contains(lines, "Process exited with code 0") || slices.Contains(lines, "Exit code: 0")
}

type workspaceFixtureTool struct {
	Type  string                 `json:"type"`
	Name  string                 `json:"name"`
	Tools []workspaceFixtureTool `json:"tools"`
}

func workspaceCanaryTool(body []byte) (string, string) {
	var request struct {
		Tools []workspaceFixtureTool `json:"tools"`
	}
	if json.Unmarshal(body, &request) != nil {
		return "", ""
	}
	for _, tool := range request.Tools {
		if isWorkspaceCanaryTool(tool) {
			return tool.Name, ""
		}
		if tool.Type == "namespace" {
			for _, nested := range tool.Tools {
				if isWorkspaceCanaryTool(nested) {
					return nested.Name, tool.Name
				}
			}
		}
	}
	return "", ""
}

func isWorkspaceCanaryTool(tool workspaceFixtureTool) bool {
	return tool.Type == "function" && (tool.Name == "exec_command" || tool.Name == "shell_command")
}

func writeWorkspaceCanaryCall(
	w http.ResponseWriter, request responsesRequest, responseID string, item map[string]any,
) {
	completed := map[string]any{
		"id": responseID, responseStatusField: responseCompleted, responseModelField: request.Model,
		"output": []any{item}, "end_turn": false,
	}
	if !request.Stream {
		writeJSON(w, http.StatusOK, completed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	added := maps.Clone(item)
	added[responseStatusField] = "in_progress"
	added["arguments"] = ""
	events := []struct {
		name string
		data map[string]any
	}{
		{"response.created", map[string]any{responseField: map[string]any{"id": responseID}}},
		{"response.output_item.added", map[string]any{responseOutputIndexField: 0, responseItemField: added}},
		{"response.function_call_arguments.delta", map[string]any{
			responseOutputIndexField: 0, responseItemIDField: item["id"], "delta": item["arguments"],
		}},
		{"response.function_call_arguments.done", map[string]any{
			responseOutputIndexField: 0, responseItemIDField: item["id"], "arguments": item["arguments"],
		}},
		{"response.output_item.done", map[string]any{responseOutputIndexField: 0, responseItemField: item}},
		{"response.completed", map[string]any{responseField: completed}},
	}
	for index, event := range events {
		event.data[responseTypeField] = event.name
		event.data[responseSequenceNumberField] = index
		writeSSE(w, event.name, event.data)
	}
}
