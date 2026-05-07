package apicompat

import "encoding/json"

// SanitizeAnthropicRequestBody removes placeholder "empty thinking" content
// blocks from a POST /v1/messages request body before it is forwarded upstream.
//
// Some Claude Code CLI versions (notably the multi-turn agentic loop after a
// 2.1.x auto-update) serialize the model's *in-progress* thinking block as
//
//	{"type":"thinking","thinking":"","signature":""}
//
// when packaging the previous turn back into messages for the next turn. The
// real Anthropic /v1/messages API rejects these blocks with a 400 schema
// validation error ("thinking content is required"), and that 400 surfaces to
// the customer mid-task as an apparent "做到一半就停了" failure.
//
// On the OpenAI translation path the existing AnthropicToResponses converter
// already discards thinking blocks entirely, so the empty-thinking artifact
// never reaches OpenAI — but for the small fraction of requests that fall
// through to a native Anthropic upstream, the schema error fires.
//
// This helper performs a single, conservative scrub at ingress, applied before
// either upstream path branches:
//
//   - Only blocks where Type=="thinking" AND Thinking=="" AND Signature=="" are
//     dropped. Any block carrying real thinking text or a server-issued
//     signature is preserved untouched, so legitimate extended-thinking
//     continuations work unchanged.
//   - The body is parsed via map[string]json.RawMessage so unknown / future
//     top-level fields (top_k, service_tier, mcp_servers, container, …) round
//     trip without loss.
//   - Surviving content blocks are also passed through as raw JSON, so any
//     block-level fields not modeled by AnthropicContentBlock are preserved.
//   - On any parse failure the original body is returned unchanged together
//     with the error, so the caller can decide whether to log it. The function
//     never substitutes a partially-modified body when something goes wrong.
//   - Idempotent: a body with no empty-thinking blocks is returned byte-for-byte
//     unchanged.
//
// Returns (possibly new body, count of blocks removed, error).
func SanitizeAnthropicRequestBody(body []byte) ([]byte, int, error) {
	if len(body) == 0 {
		return body, 0, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return body, 0, err
	}

	msgsRaw, ok := top["messages"]
	if !ok || !looksLikeJSONArray(msgsRaw) {
		return body, 0, nil
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return body, 0, err
	}

	totalRemoved := 0
	anyChanged := false

	for i, msgRaw := range msgs {
		newMsg, removed, err := sanitizeMessage(msgRaw)
		if err != nil {
			// Best-effort: a single malformed message should not abort the whole
			// request. Leave it as-is and let the upstream return its native
			// error, which preserves existing error semantics.
			continue
		}
		if removed == 0 {
			continue
		}
		msgs[i] = newMsg
		totalRemoved += removed
		anyChanged = true
	}

	if !anyChanged {
		return body, 0, nil
	}

	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return body, 0, err
	}
	top["messages"] = newMsgs

	out, err := json.Marshal(top)
	if err != nil {
		return body, 0, err
	}
	return out, totalRemoved, nil
}

// sanitizeMessage processes a single Anthropic message object and returns the
// rewritten message bytes (when content blocks were dropped) plus the count of
// removed blocks. A message whose content is not a JSON array (e.g. a plain
// string content) is returned with removed=0 and the original bytes.
func sanitizeMessage(msgRaw json.RawMessage) (json.RawMessage, int, error) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return msgRaw, 0, err
	}

	contentRaw, ok := msg["content"]
	if !ok || !looksLikeJSONArray(contentRaw) {
		return msgRaw, 0, nil
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return msgRaw, 0, err
	}

	kept := make([]json.RawMessage, 0, len(blocks))
	removed := 0
	for _, b := range blocks {
		if isEmptyThinkingBlockRaw(b) {
			removed++
			continue
		}
		kept = append(kept, b)
	}

	if removed == 0 {
		return msgRaw, 0, nil
	}

	newContent, err := json.Marshal(kept)
	if err != nil {
		return msgRaw, 0, err
	}
	msg["content"] = newContent

	newMsg, err := json.Marshal(msg)
	if err != nil {
		return msgRaw, 0, err
	}
	return newMsg, removed, nil
}

// isEmptyThinkingBlockRaw reports whether a raw content block is a placeholder
// thinking block: Type=="thinking", empty Thinking, and no Signature. A small
// dedicated probe struct is used so unrelated block fields (text, tool_use_id,
// source, …) do not affect the decision.
func isEmptyThinkingBlockRaw(raw json.RawMessage) bool {
	var probe struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Type == "thinking" && probe.Thinking == "" && probe.Signature == ""
}

// looksLikeJSONArray reports whether the first non-whitespace byte of raw is
// '['. Used to cheaply distinguish array content from string / null / object
// without paying for a full unmarshal.
func looksLikeJSONArray(raw []byte) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}
