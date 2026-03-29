package services

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPrepareCodexResponseChainInjectsStoreOnFirstRequest(t *testing.T) {
	globalCodexResponseChainStore.Reset()
	t.Cleanup(globalCodexResponseChainStore.Reset)

	provider := Provider{
		APIURL: "https://api.example.com",
		APIKey: "test-api-key",
	}
	body := []byte(`{"model":"gpt-5.1","input":"hello","instructions":"system"}`)

	nextBody, plan, err := prepareCodexResponseChain(provider, "/v1/responses", map[string]string{}, body)
	if err != nil {
		t.Fatalf("prepareCodexResponseChain returned error: %v", err)
	}
	if !plan.Active {
		t.Fatal("expected active response chain plan")
	}
	if plan.SessionKey == "" {
		t.Fatal("expected generated session key")
	}
	if !gjson.GetBytes(nextBody, "store").Bool() {
		t.Fatal("expected store=true on first request")
	}
	if gjson.GetBytes(nextBody, "previous_response_id").Exists() {
		t.Fatal("did not expect previous_response_id on first request")
	}
	if got := gjson.GetBytes(nextBody, "input").String(); got != "hello" {
		t.Fatalf("input = %q, want %q", got, "hello")
	}
}

func TestPrepareCodexResponseChainRewritesSuffixAndPreservesInstructionsAndTools(t *testing.T) {
	globalCodexResponseChainStore.Reset()
	t.Cleanup(globalCodexResponseChainStore.Reset)

	provider := Provider{
		APIURL: "https://api.example.com",
		APIKey: "test-api-key",
	}
	headers := map[string]string{
		codexResponseChainSessionHeader: "session-1",
	}

	firstBody := []byte(`{"model":"gpt-5.1","input":[{"role":"user","content":"hello"}],"instructions":"system","tools":[{"type":"function","name":"tool_a"}]}`)
	_, canonical, _, ok := extractCodexResponseChainInput(firstBody)
	if !ok {
		t.Fatal("expected first request input to be canonicalizable")
	}
	namespace := codexResponseChainNamespace(provider, "session-1")
	globalCodexResponseChainStore.Set(namespace, codexResponseChainState{
		LastResponseID:     "resp_1",
		LastInputCanonical: canonical,
		LastInputType:      "array",
		Model:              "gpt-5.1",
		InstructionsRaw:    json.RawMessage(`"system"`),
		ToolsRaw:           json.RawMessage(`[{"type":"function","name":"tool_a"}]`),
	})

	secondBody := []byte(`{"model":"gpt-5.1","input":[{"role":"user","content":"hello"},{"role":"user","content":"world"}]}`)
	nextBody, plan, err := prepareCodexResponseChain(provider, "/v1/responses", headers, secondBody)
	if err != nil {
		t.Fatalf("prepareCodexResponseChain returned error: %v", err)
	}
	if !plan.Active {
		t.Fatal("expected active response chain plan")
	}
	if got := gjson.GetBytes(nextBody, "previous_response_id").String(); got != "resp_1" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp_1")
	}
	if got := gjson.GetBytes(nextBody, "input.#").Int(); got != 1 {
		t.Fatalf("suffix input length = %d, want 1", got)
	}
	if got := gjson.GetBytes(nextBody, "input.0.content").String(); got != "world" {
		t.Fatalf("suffix input content = %q, want %q", got, "world")
	}
	if got := gjson.GetBytes(nextBody, "instructions").String(); got != "system" {
		t.Fatalf("instructions = %q, want %q", got, "system")
	}
	if got := gjson.GetBytes(nextBody, "tools.0.name").String(); got != "tool_a" {
		t.Fatalf("tools[0].name = %q, want %q", got, "tool_a")
	}
}

func TestPrepareCodexResponseChainFallsBackWhenHistoryDiverges(t *testing.T) {
	globalCodexResponseChainStore.Reset()
	t.Cleanup(globalCodexResponseChainStore.Reset)

	provider := Provider{
		APIURL: "https://api.example.com",
		APIKey: "test-api-key",
	}
	headers := map[string]string{
		codexResponseChainSessionHeader: "session-2",
	}

	firstBody := []byte(`{"model":"gpt-5.1","input":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`)
	_, canonical, _, ok := extractCodexResponseChainInput(firstBody)
	if !ok {
		t.Fatal("expected first request input to be canonicalizable")
	}
	namespace := codexResponseChainNamespace(provider, "session-2")
	globalCodexResponseChainStore.Set(namespace, codexResponseChainState{
		LastResponseID:     "resp_prev",
		LastInputCanonical: canonical,
		LastInputType:      "array",
		Model:              "gpt-5.1",
	})

	divergedBody := []byte(`{"model":"gpt-5.1","input":[{"role":"user","content":"a"},{"role":"assistant","content":"c"}]}`)
	nextBody, _, err := prepareCodexResponseChain(provider, "/v1/responses", headers, divergedBody)
	if err != nil {
		t.Fatalf("prepareCodexResponseChain returned error: %v", err)
	}
	if gjson.GetBytes(nextBody, "previous_response_id").Exists() {
		t.Fatal("did not expect previous_response_id when history diverges")
	}
	if got := gjson.GetBytes(nextBody, "input.#").Int(); got != 2 {
		t.Fatalf("expected full input to be preserved, got length %d", got)
	}
}

func TestCodexResponseChainCaptureParsesResponseCreated(t *testing.T) {
	capture := &codexResponseChainCapture{}
	capture.ObservePayload("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream_1\"}}\n\n")
	if got := capture.GetResponseID(); got != "resp_stream_1" {
		t.Fatalf("response id = %q, want %q", got, "resp_stream_1")
	}
}
