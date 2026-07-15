// Package openai provides helpers and types for OpenAI API integration.
package openai

import _ "embed"

const (
	ModelGPT56Sol   = "gpt-5.6-sol"
	ModelGPT56Terra = "gpt-5.6-terra"
	ModelGPT56Luna  = "gpt-5.6-luna"
)

// Model represents an OpenAI model
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// DefaultModels OpenAI models list
var DefaultModels = []Model{
	{ID: "gpt-5.5", Object: "model", Created: 1776873600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5"},
	{ID: "gpt-5.4", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4"},
	{ID: "gpt-5.4-mini", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Mini"},
	{ID: "gpt-5.3-codex", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex"},
	{ID: "gpt-5.3-codex-spark", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex Spark"},
	{ID: "gpt-5.2", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.2"},
	{ID: "gpt-image-1", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1"},
	{ID: "gpt-image-1.5", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5"},
	{ID: "gpt-image-2", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2"},
}

// DefaultModelIDs returns the default model ID list
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel default model for testing OpenAI accounts
const DefaultTestModel = "gpt-5.4"

// XAIDefaultTestModel is the conservative default for xAI-compatible accounts.
const XAIDefaultTestModel = "grok-4.5"

var xaiOAuthTextModels = []string{
	"grok-4.20-0309-non-reasoning",
	"grok-4.20-0309-reasoning",
	"grok-4.20-multi-agent-0309",
	"grok-4.3",
	"grok-4.5",
	"grok-build-0.1",
}

// XAIOAuthTextModelIDs returns the text models verified on api.x.ai with xAI OAuth.
func XAIOAuthTextModelIDs() []string {
	return append([]string(nil), xaiOAuthTextModels...)
}

// IsXAIOAuthTextModel reports whether model is in the verified api.x.ai OAuth text set.
func IsXAIOAuthTextModel(model string) bool {
	for _, candidate := range xaiOAuthTextModels {
		if model == candidate {
			return true
		}
	}
	return false
}

// DefaultInstructions default instructions for non-Codex CLI requests
// Content loaded from instructions.txt at compile time
//
//go:embed instructions.txt
var DefaultInstructions string
