package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// helper: re-decode result body so test assertions are key-order-agnostic
// (Go's encoding/json sorts map keys alphabetically on marshal).
func decodeRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode result: %v\nbody: %s", err, string(body))
	}
	return got
}

func TestSanitize_EmptyBody_NoOp(t *testing.T) {
	out, removed, err := SanitizeAnthropicRequestBody(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if out != nil {
		t.Fatalf("want nil out, got %v", out)
	}
}

func TestSanitize_BadJSON_ReturnsOriginal(t *testing.T) {
	in := []byte("{not json")
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err == nil {
		t.Fatalf("expected error for bad JSON")
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if string(out) != string(in) {
		t.Fatalf("body mutated on error: got %q want %q", string(out), string(in))
	}
}

func TestSanitize_NoMessagesField_NoOp(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","max_tokens":100}`)
	out, removed, _ := SanitizeAnthropicRequestBody(in)
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if string(out) != string(in) {
		t.Fatalf("body should be unchanged when messages absent")
	}
}

func TestSanitize_StringContent_NoOp(t *testing.T) {
	// content is a plain string — many older clients send this shape.
	in := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if string(out) != string(in) {
		t.Fatalf("string-content body should be byte-identical, got %q", string(out))
	}
}

func TestSanitize_NoThinkingBlocks_Idempotent(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if string(out) != string(in) {
		t.Fatalf("body should be unchanged when no thinking blocks present")
	}
}

func TestSanitize_EmptyThinkingBlockStripped(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"the answer"}` +
		`]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1", removed)
	}
	got := decodeRequest(t, out)
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len=%d want 1", len(msgs))
	}
	msg, _ := msgs[0].(map[string]any)
	content, _ := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len=%d want 1 (only text remains)", len(content))
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("surviving block type=%v want text", first["type"])
	}
}

func TestSanitize_ThinkingBlockWithoutSignatureKey_Stripped(t *testing.T) {
	// Some clients omit the signature key entirely instead of sending "".
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"text","text":"hi"}` +
		`]}` +
		`]}`)
	_, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1 (missing signature key counts as empty)", removed)
	}
}

func TestSanitize_NonEmptyThinkingPreserved(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"let me think about this carefully"},` +
		`{"type":"text","text":"answer"}` +
		`]}` +
		`]}`)
	_, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0 (thinking text present must be preserved)", removed)
	}
}

func TestSanitize_SignedThinkingPreserved(t *testing.T) {
	// Real extended-thinking continuation: thinking text is empty but server
	// issued a signature. Must NOT be stripped — Anthropic verifies signature
	// on the next turn.
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":"sig_abc123"},` +
		`{"type":"text","text":"hi"}` +
		`]}` +
		`]}`)
	_, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0 (signed thinking block must survive)", removed)
	}
}

func TestSanitize_MixedBlocks_OnlyEmptyThinkingDropped(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"step 1"},` +
		`{"type":"tool_use","id":"toolu_01","name":"bash","input":{"cmd":"ls"}},` +
		`{"type":"thinking","thinking":"","signature":""}` +
		`]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d want 2", removed)
	}
	got := decodeRequest(t, out)
	msgs, _ := got["messages"].([]any)
	msg, _ := msgs[0].(map[string]any)
	content, _ := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len=%d want 2 (text + tool_use)", len(content))
	}
	types := []string{}
	for _, c := range content {
		m, _ := c.(map[string]any)
		t, _ := m["type"].(string)
		types = append(types, t)
	}
	if types[0] != "text" || types[1] != "tool_use" {
		t.Fatalf("surviving types=%v want [text tool_use]", types)
	}
}

func TestSanitize_MultipleMessages_AllScrubbed(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"q1"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"a1"}]},` +
		`{"role":"user","content":[{"type":"text","text":"q2"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"a2"}]}` +
		`]}`)
	_, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d want 2 (one per assistant turn)", removed)
	}
}

func TestSanitize_PreservesUnknownTopLevelFields(t *testing.T) {
	// top_k, service_tier, container, mcp_servers — these are real or possible
	// Anthropic fields not modeled in AnthropicRequest. We must round-trip them.
	in := []byte(`{"model":"claude-opus-4-7","top_k":40,"service_tier":"priority",` +
		`"some_future_field":{"x":1},"messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"hi"}]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1", removed)
	}
	got := decodeRequest(t, out)
	if got["top_k"] != float64(40) {
		t.Fatalf("top_k missing or wrong: %v", got["top_k"])
	}
	if got["service_tier"] != "priority" {
		t.Fatalf("service_tier missing: %v", got["service_tier"])
	}
	if _, ok := got["some_future_field"]; !ok {
		t.Fatalf("unknown field dropped on round-trip")
	}
}

func TestSanitize_PreservesUnknownBlockFields(t *testing.T) {
	// A block carries a hypothetical future field "cache_control"; surviving
	// blocks must be passed through byte-for-byte (modulo key reorder).
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"user","content":[` +
		`{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}` +
		`]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed=%d want 0", removed)
	}
	if !strings.Contains(string(out), "cache_control") {
		t.Fatalf("cache_control field dropped from surviving block: %s", string(out))
	}
}

func TestSanitize_PreservesSystemAndTools(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","system":"you are helpful",` +
		`"tools":[{"name":"bash","description":"run shell","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"hi"}]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1", removed)
	}
	got := decodeRequest(t, out)
	if got["system"] != "you are helpful" {
		t.Fatalf("system field lost: %v", got["system"])
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools array lost: %v", got["tools"])
	}
}

func TestSanitize_Idempotent(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""},` +
		`{"type":"text","text":"hi"}]}` +
		`]}`)
	first, removed1, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("first pass error: %v", err)
	}
	if removed1 != 1 {
		t.Fatalf("first pass removed=%d want 1", removed1)
	}
	second, removed2, err := SanitizeAnthropicRequestBody(first)
	if err != nil {
		t.Fatalf("second pass error: %v", err)
	}
	if removed2 != 0 {
		t.Fatalf("second pass removed=%d want 0 (idempotency)", removed2)
	}
	if string(second) != string(first) {
		t.Fatalf("idempotency broken: second pass mutated body\nfirst:  %s\nsecond: %s", string(first), string(second))
	}
}

func TestSanitize_AssistantOnlyEmptyThinking_LeavesEmptyArray(t *testing.T) {
	// Pathological: an assistant turn whose only block is empty thinking.
	// We strip it and leave content as []. This is what the API would have
	// rejected anyway — better an empty content (likely a different error)
	// than the misleading thinking-schema error.
	in := []byte(`{"model":"claude-opus-4-7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"","signature":""}]}` +
		`]}`)
	out, removed, err := SanitizeAnthropicRequestBody(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want 1", removed)
	}
	got := decodeRequest(t, out)
	msgs, _ := got["messages"].([]any)
	msg, _ := msgs[0].(map[string]any)
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content should remain an array, got %T", msg["content"])
	}
	if len(content) != 0 {
		t.Fatalf("content len=%d want 0", len(content))
	}
}
