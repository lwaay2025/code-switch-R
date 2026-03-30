package services

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexResponseChainSessionHeader = "X-CodeSwitch-Session-Key"
const codexResponseChainTTL = time.Hour
const codexResponseChainCleanupInterval = 10 * time.Minute

var codexResponseChainNow = time.Now

type codexResponseChainState struct {
	LastResponseID     string
	LastInputCanonical string
	LastInputType      string
	Model              string
	InstructionsRaw    json.RawMessage
	ToolsRaw           json.RawMessage
	InstructionsHash   string
	ToolSchemaHash     string
	Disabled           bool
	LastSeen           time.Time
}

type codexResponseChainStore struct {
	mu      sync.Mutex
	entries map[string]codexResponseChainState
	stopCh  chan struct{}
	wg      sync.WaitGroup
	started bool
}

type codexResponseChainPlan struct {
	Active     bool
	SessionKey string
	Namespace  string
	State      codexResponseChainState
}

type codexResponseChainCapture struct {
	mu         sync.Mutex
	ResponseID string
	HasOutput  bool
	Completed  bool
}

var globalCodexResponseChainStore = newCodexResponseChainStore()

func newCodexResponseChainStore() *codexResponseChainStore {
	return &codexResponseChainStore{
		entries: make(map[string]codexResponseChainState),
	}
}

func prepareCodexResponseChain(provider Provider, endpoint string, headers map[string]string, body []byte) ([]byte, codexResponseChainPlan, error) {
	if !isCodexResponseChainEligibleEndpoint(endpoint) {
		return body, codexResponseChainPlan{}, nil
	}
	if !provider.CodexResponseChainEnabled {
		return body, codexResponseChainPlan{}, nil
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || !gjson.Valid(trimmed) {
		return body, codexResponseChainPlan{}, nil
	}

	sessionKey := resolveCodexResponseChainSessionKey(headers, body)
	namespace := codexResponseChainNamespace(provider, sessionKey)
	if sessionKey == "" || namespace == "" {
		return body, codexResponseChainPlan{}, nil
	}

	nextBody := body
	var err error
	nextBody, err = ensureCodexResponseStore(nextBody)
	if err != nil {
		return body, codexResponseChainPlan{}, err
	}

	currentInputRaw, currentInputCanonical, currentInputType, hasInput := extractCodexResponseChainInput(body)
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())

	state := codexResponseChainState{
		LastInputCanonical: currentInputCanonical,
		LastInputType:      currentInputType,
		Model:              model,
		InstructionsRaw:    extractCodexResponseChainRawField(nextBody, "instructions"),
		ToolsRaw:           extractCodexResponseChainRawField(nextBody, "tools"),
		LastSeen:           codexResponseChainNow().UTC(),
	}
	state.InstructionsHash = shortStableHash(string(state.InstructionsRaw))
	state.ToolSchemaHash = shortStableHash(string(state.ToolsRaw))

	if explicitPrev := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()); explicitPrev != "" {
		plan := codexResponseChainPlan{
			Active:     true,
			SessionKey: sessionKey,
			Namespace:  namespace,
			State:      state,
		}
		return nextBody, plan, nil
	}

	prevState, ok := globalCodexResponseChainStore.Get(namespace)
	if !ok {
		plan := codexResponseChainPlan{
			Active:     true,
			SessionKey: sessionKey,
			Namespace:  namespace,
			State:      state,
		}
		return nextBody, plan, nil
	}

	if len(state.InstructionsRaw) == 0 && len(prevState.InstructionsRaw) > 0 {
		nextBody, err = setCodexResponseChainRawField(nextBody, "instructions", prevState.InstructionsRaw)
		if err != nil {
			return body, codexResponseChainPlan{}, err
		}
		state.InstructionsRaw = append(json.RawMessage(nil), prevState.InstructionsRaw...)
		state.InstructionsHash = shortStableHash(string(state.InstructionsRaw))
	}
	if len(state.ToolsRaw) == 0 && len(prevState.ToolsRaw) > 0 {
		nextBody, err = setCodexResponseChainRawField(nextBody, "tools", prevState.ToolsRaw)
		if err != nil {
			return body, codexResponseChainPlan{}, err
		}
		state.ToolsRaw = append(json.RawMessage(nil), prevState.ToolsRaw...)
		state.ToolSchemaHash = shortStableHash(string(state.ToolsRaw))
	}
	if prevState.Disabled {
		state.Disabled = true
		plan := codexResponseChainPlan{
			Active:     true,
			SessionKey: sessionKey,
			Namespace:  namespace,
			State:      state,
		}
		return nextBody, plan, nil
	}

	if !codexResponseChainModelsCompatible(prevState.Model, model) || prevState.LastResponseID == "" || !hasInput {
		plan := codexResponseChainPlan{
			Active:     true,
			SessionKey: sessionKey,
			Namespace:  namespace,
			State:      state,
		}
		return nextBody, plan, nil
	}

	suffixRaw, diffOK := buildCodexResponseChainInputSuffix(prevState.LastInputCanonical, currentInputRaw)
	if !diffOK {
		plan := codexResponseChainPlan{
			Active:     true,
			SessionKey: sessionKey,
			Namespace:  namespace,
			State:      state,
		}
		return nextBody, plan, nil
	}

	nextBody, err = sjson.SetBytes(nextBody, "previous_response_id", prevState.LastResponseID)
	if err != nil {
		return body, codexResponseChainPlan{}, err
	}
	nextBody, err = sjson.SetRawBytes(nextBody, "input", suffixRaw)
	if err != nil {
		return body, codexResponseChainPlan{}, err
	}

	plan := codexResponseChainPlan{
		Active:     true,
		SessionKey: sessionKey,
		Namespace:  namespace,
		State:      state,
	}
	return nextBody, plan, nil
}

func isCodexResponseChainEligibleEndpoint(endpoint string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(endpoint))
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "/responses") && !strings.Contains(trimmed, "/responses/compact")
}

func resolveCodexResponseChainSessionKey(headers map[string]string, body []byte) string {
	for _, name := range []string{
		codexResponseChainSessionHeader,
		"Conversation_id",
		"Session_id",
		"session_id",
		"session-id",
	} {
		if key := getHeaderValueFold(headers, name); key != "" {
			return key
		}
	}
	for _, path := range []string{"prompt_cache_key", "session_id", "sessionId", "conversation_id", "conversationId"} {
		if key := strings.TrimSpace(gjson.GetBytes(body, path).String()); key != "" {
			return key
		}
	}
	return uuid.NewString()
}

func codexResponseChainNamespace(provider Provider, sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	apiURL := strings.TrimSpace(provider.APIURL)
	apiKey := strings.TrimSpace(provider.APIKey)
	return apiURL + "|" + apiKey + "|" + sessionKey
}

func ensureCodexResponseStore(body []byte) ([]byte, error) {
	current := gjson.GetBytes(body, "store")
	if current.Exists() && current.Type == gjson.True {
		return body, nil
	}
	return sjson.SetBytes(body, "store", true)
}

func extractCodexResponseChainRawField(body []byte, path string) json.RawMessage {
	result := gjson.GetBytes(body, path)
	if !result.Exists() {
		return nil
	}
	raw := strings.TrimSpace(result.Raw)
	if raw == "" {
		return nil
	}
	return append(json.RawMessage(nil), []byte(raw)...)
}

func setCodexResponseChainRawField(body []byte, path string, raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return body, nil
	}
	return sjson.SetRawBytes(body, path, raw)
}

func extractCodexResponseChainInput(body []byte) (json.RawMessage, string, string, bool) {
	result := gjson.GetBytes(body, "input")
	if !result.Exists() {
		return nil, "", "", false
	}

	raw := strings.TrimSpace(result.Raw)
	if raw == "" {
		return nil, "", "", false
	}

	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, "", "", false
	}

	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, "", "", false
	}

	switch payload.(type) {
	case string:
		return json.RawMessage(raw), string(canonical), "string", true
	case []any:
		return json.RawMessage(raw), string(canonical), "array", true
	default:
		return json.RawMessage(raw), string(canonical), "other", true
	}
}

func buildCodexResponseChainInputSuffix(previousCanonical string, currentRaw json.RawMessage) (json.RawMessage, bool) {
	previousCanonical = strings.TrimSpace(previousCanonical)
	if previousCanonical == "" || len(currentRaw) == 0 {
		return nil, false
	}

	var previous any
	if err := json.Unmarshal([]byte(previousCanonical), &previous); err != nil {
		return nil, false
	}

	var current any
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		return nil, false
	}

	switch prev := previous.(type) {
	case string:
		curr, ok := current.(string)
		if !ok || !strings.HasPrefix(curr, prev) {
			return nil, false
		}
		suffix, err := json.Marshal(curr[len(prev):])
		if err != nil {
			return nil, false
		}
		return json.RawMessage(suffix), true
	case []any:
		curr, ok := current.([]any)
		if !ok || len(curr) < len(prev) {
			return nil, false
		}
		for i := range prev {
			prevItem, err := json.Marshal(prev[i])
			if err != nil {
				return nil, false
			}
			currItem, err := json.Marshal(curr[i])
			if err != nil {
				return nil, false
			}
			if string(prevItem) != string(currItem) {
				return nil, false
			}
		}
		suffixItems := trimCodexResponseChainReplayItems(curr[len(prev):])
		if len(suffixItems) == 0 {
			return nil, false
		}
		suffix, err := json.Marshal(suffixItems)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(suffix), true
	default:
		return nil, false
	}
}

func trimCodexResponseChainReplayItems(items []any) []any {
	if len(items) == 0 {
		return items
	}

	start := 0
	for start < len(items) && isCodexResponseChainReplayOnlyItem(items[start]) {
		start++
	}
	return items[start:]
}

func isCodexResponseChainReplayOnlyItem(item any) bool {
	obj, ok := item.(map[string]any)
	if !ok {
		return false
	}

	role := strings.ToLower(strings.TrimSpace(stringValue(obj["role"])))
	if role == "assistant" {
		return true
	}
	if role == "user" || role == "tool" {
		return false
	}

	itemType := strings.ToLower(strings.TrimSpace(stringValue(obj["type"])))
	switch itemType {
	case "":
		return false
	case "message":
		return role == "assistant"
	case "function_call_output", "computer_call_output", "tool_result":
		return false
	case "function_call", "custom_tool_call", "computer_call", "code_interpreter_call", "web_search_call", "image_generation_call", "mcp_call", "reasoning":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func codexResponseChainModelsCompatible(previousModel, currentModel string) bool {
	previousModel = strings.TrimSpace(previousModel)
	currentModel = strings.TrimSpace(currentModel)
	return previousModel == "" || currentModel == "" || previousModel == currentModel
}

func extractCodexResponseID(body []byte) string {
	for _, path := range []string{"id", "response.id"} {
		if id := strings.TrimSpace(gjson.GetBytes(body, path).String()); id != "" {
			return id
		}
	}
	return ""
}

func (c *codexResponseChainCapture) ObservePayload(payload string) {
	if c == nil {
		return
	}
	lines := strings.Split(payload, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		eventType := strings.TrimSpace(gjson.Get(data, "type").String())
		if eventType != "" && eventType != "response.created" && !strings.HasPrefix(eventType, "response.") {
			continue
		}
		hasOutput := codexResponsePayloadHasOutput([]byte(data))
		completed := strings.EqualFold(eventType, "response.completed") ||
			strings.EqualFold(strings.TrimSpace(gjson.Get(data, "response.status").String()), "completed") ||
			strings.EqualFold(strings.TrimSpace(gjson.Get(data, "status").String()), "completed")
		responseID := strings.TrimSpace(gjson.Get(data, "response.id").String())
		if responseID == "" {
			responseID = strings.TrimSpace(gjson.Get(data, "id").String())
		}
		c.mu.Lock()
		if c.ResponseID == "" && responseID != "" {
			c.ResponseID = responseID
		}
		if hasOutput {
			c.HasOutput = true
		}
		if completed {
			c.Completed = true
		}
		c.mu.Unlock()
	}
}

func (c *codexResponseChainCapture) GetResponseID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ResponseID
}

func (c *codexResponseChainCapture) IsUsableSuccess() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ResponseID != "" && (c.HasOutput || c.Completed)
}

func persistCodexResponseChain(plan codexResponseChainPlan, responseID string) {
	if !plan.Active {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || strings.TrimSpace(plan.Namespace) == "" {
		return
	}

	state := plan.State
	state.LastResponseID = responseID
	state.LastSeen = codexResponseChainNow().UTC()
	globalCodexResponseChainStore.Set(plan.Namespace, state)
}

func disableCodexResponseChain(plan codexResponseChainPlan) codexResponseChainPlan {
	if !plan.Active || strings.TrimSpace(plan.Namespace) == "" {
		return plan
	}

	plan.State.Disabled = true
	plan.State.LastResponseID = ""
	plan.State.LastSeen = codexResponseChainNow().UTC()
	globalCodexResponseChainStore.Set(plan.Namespace, plan.State)
	return plan
}

func rebuildCodexResponseChainFallbackBody(originalBody []byte, plan codexResponseChainPlan) ([]byte, error) {
	nextBody := originalBody
	var err error

	nextBody, err = ensureCodexResponseStore(nextBody)
	if err != nil {
		return nil, err
	}

	if len(plan.State.InstructionsRaw) > 0 && !gjson.GetBytes(nextBody, "instructions").Exists() {
		nextBody, err = setCodexResponseChainRawField(nextBody, "instructions", plan.State.InstructionsRaw)
		if err != nil {
			return nil, err
		}
	}
	if len(plan.State.ToolsRaw) > 0 && !gjson.GetBytes(nextBody, "tools").Exists() {
		nextBody, err = setCodexResponseChainRawField(nextBody, "tools", plan.State.ToolsRaw)
		if err != nil {
			return nil, err
		}
	}
	if gjson.GetBytes(nextBody, "previous_response_id").Exists() {
		nextBody, err = sjson.DeleteBytes(nextBody, "previous_response_id")
		if err != nil {
			return nil, err
		}
	}

	return nextBody, nil
}

func (s *codexResponseChainStore) Get(namespace string) (codexResponseChainState, bool) {
	if s == nil {
		return codexResponseChainState{}, false
	}
	s.Start()

	now := codexResponseChainNow().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state, ok := s.entries[namespace]
	if !ok {
		return codexResponseChainState{}, false
	}
	state.LastSeen = now
	s.entries[namespace] = state
	return cloneCodexResponseChainState(state), true
}

func (s *codexResponseChainStore) Set(namespace string, state codexResponseChainState) {
	if s == nil || strings.TrimSpace(namespace) == "" {
		return
	}
	s.Start()

	state.LastSeen = codexResponseChainNow().UTC()
	s.mu.Lock()
	s.cleanupExpiredLocked(state.LastSeen)
	s.entries[namespace] = cloneCodexResponseChainState(state)
	s.mu.Unlock()
}

func cloneCodexResponseChainState(state codexResponseChainState) codexResponseChainState {
	state.InstructionsRaw = append(json.RawMessage(nil), state.InstructionsRaw...)
	state.ToolsRaw = append(json.RawMessage(nil), state.ToolsRaw...)
	return state
}

func (s *codexResponseChainStore) Start() {
	if s == nil {
		return
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.stopCh = make(chan struct{})
	s.started = true
	s.wg.Add(1)
	stopCh := s.stopCh
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(codexResponseChainCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.CleanupExpired(codexResponseChainNow().UTC())
			case <-stopCh:
				return
			}
		}
	}()
}

func (s *codexResponseChainStore) Stop() {
	if s == nil {
		return
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	stopCh := s.stopCh
	s.stopCh = nil
	s.started = false
	s.mu.Unlock()

	close(stopCh)
	s.wg.Wait()
}

func (s *codexResponseChainStore) Reset() {
	if s == nil {
		return
	}
	s.Stop()
	s.mu.Lock()
	s.entries = make(map[string]codexResponseChainState)
	s.mu.Unlock()
}

func (s *codexResponseChainStore) CleanupExpired(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupExpiredLocked(now)
}

func (s *codexResponseChainStore) cleanupExpiredLocked(now time.Time) int {
	removed := 0
	for namespace, state := range s.entries {
		if now.Sub(state.LastSeen) >= codexResponseChainTTL {
			delete(s.entries, namespace)
			removed++
		}
	}
	return removed
}

func getHeaderValueFold(headers map[string]string, name string) string {
	if len(headers) == 0 {
		return ""
	}
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexResponsePayloadHasOutput(body []byte) bool {
	for _, path := range []string{
		"output",
		"response.output",
		"output_text",
		"response.output_text",
	} {
		result := gjson.GetBytes(body, path)
		if !result.Exists() {
			continue
		}
		switch result.Type {
		case gjson.String:
			if strings.TrimSpace(result.String()) != "" {
				return true
			}
		case gjson.JSON:
			if len(result.Array()) > 0 {
				return true
			}
			if strings.TrimSpace(result.Raw) != "" && result.Raw != "[]" && result.Raw != "{}" {
				return true
			}
		default:
			if strings.TrimSpace(result.Raw) != "" {
				return true
			}
		}
	}

	for _, path := range []string{
		"output.0.content.0.text",
		"response.output.0.content.0.text",
		"output.0.content",
		"response.output.0.content",
	} {
		if strings.TrimSpace(gjson.GetBytes(body, path).String()) != "" {
			return true
		}
	}

	return false
}

func isUsableCodexResponseBody(body []byte) bool {
	responseID := extractCodexResponseID(body)
	if responseID == "" {
		return false
	}
	if codexResponsePayloadHasOutput(body) {
		return true
	}

	for _, path := range []string{"status", "response.status"} {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, path).String()), "completed") {
			return true
		}
	}

	return false
}

func logCodexResponseChainRewrite(endpoint string, plan codexResponseChainPlan, body []byte) {
	if !plan.Active {
		return
	}
	prevID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	store := gjson.GetBytes(body, "store").Bool()
	inputCount := gjson.GetBytes(body, "input.#").Int()
	instructionsExists := gjson.GetBytes(body, "instructions").Exists()
	toolsCount := gjson.GetBytes(body, "tools.#").Int()
	fmt.Printf(
		"[CodexChain] endpoint=%s session=%s prev=%s store=%t input_count=%d instructions=%t tools=%d\n",
		endpoint,
		plan.SessionKey,
		prevID,
		store,
		inputCount,
		instructionsExists,
		toolsCount,
	)
}
