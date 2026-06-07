package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestPatchEmptyResponsesTerminalOutput verifies that a streaming terminal
// event whose response.output is empty is reconstructed from accumulated SSE
// deltas, while populated or content-less events are left untouched.
func TestPatchEmptyResponsesTerminalOutput(t *testing.T) {
	deltas := "" +
		`data: {"type":"response.output_text.delta","delta":"hello "}` + "\n" +
		`data: {"type":"response.output_text.delta","delta":"world"}` + "\n"

	tests := []struct {
		name        string
		terminal    string
		accumulated string
		wantPatched bool
		wantText    string
	}{
		{
			name:        "empty output array is reconstructed from deltas",
			terminal:    `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`,
			accumulated: deltas,
			wantPatched: true,
			wantText:    "hello world",
		},
		{
			name:        "missing output key is reconstructed from deltas",
			terminal:    `{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
			accumulated: deltas,
			wantPatched: true,
			wantText:    "hello world",
		},
		{
			name:        "response.done is also handled",
			terminal:    `{"type":"response.done","response":{"id":"r1","output":[]}}`,
			accumulated: deltas,
			wantPatched: true,
			wantText:    "hello world",
		},
		{
			name:        "already populated output is left untouched",
			terminal:    `{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"orig"}]}]}}`,
			accumulated: deltas,
			wantPatched: false,
			wantText:    "orig",
		},
		{
			name:        "no reconstructable content is a no-op",
			terminal:    `{"type":"response.completed","response":{"output":[]}}`,
			accumulated: `data: {"type":"response.created","response":{"id":"r1"}}` + "\n",
			wantPatched: false,
			wantText:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, changed := patchEmptyResponsesTerminalOutput([]byte(tt.terminal), tt.accumulated)
			if changed != tt.wantPatched {
				t.Fatalf("changed = %v, want %v (result: %s)", changed, tt.wantPatched, patched)
			}
			if tt.wantText != "" {
				got := gjson.GetBytes(patched, "response.output.0.content.0.text").String()
				if got != tt.wantText {
					t.Fatalf("reconstructed text = %q, want %q (result: %s)", got, tt.wantText, patched)
				}
				if msgType := gjson.GetBytes(patched, "response.output.0.type").String(); msgType != "message" {
					t.Fatalf("output[0].type = %q, want message", msgType)
				}
			}
		})
	}
}
