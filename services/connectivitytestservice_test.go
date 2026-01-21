package services

import (
	"encoding/json"
	"testing"
)

func TestConnectivityBuildTestRequestCodexOmitsMaxTokens(t *testing.T) {
	cts := &ConnectivityTestService{}
	provider := Provider{}

	body, contentField := cts.buildTestRequest("codex", &provider)
	if contentField != "" {
		t.Fatalf("unexpected content field: %s", contentField)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	if _, ok := req["max_tokens"]; ok {
		t.Fatal("max_tokens should not be included for Codex connectivity tests")
	}

	if _, ok := req["max_output_tokens"]; !ok {
		t.Fatal("max_output_tokens should be included for Codex connectivity tests")
	}

	input, ok := req["input"].([]interface{})
	if !ok || len(input) == 0 {
		t.Fatalf("input should be a non-empty array, got: %T %v", req["input"], req["input"])
	}

	first, ok := input[0].(map[string]interface{})
	if !ok {
		t.Fatalf("input[0] should be an object, got: %T", input[0])
	}

	role, _ := first["role"].(string)
	content, _ := first["content"].(string)
	if role == "" || content == "" {
		t.Fatalf("input[0] missing role/content: %v", first)
	}
}
