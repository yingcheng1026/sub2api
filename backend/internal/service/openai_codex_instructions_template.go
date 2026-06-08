package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

const openAICompatKnowledgeCutoffGuardMarker = "HFC GPT compatibility note:"

type forcedCodexInstructionsTemplateData struct {
	ExistingInstructions string
	OriginalModel        string
	NormalizedModel      string
	BillingModel         string
	UpstreamModel        string
}

func applyForcedCodexInstructionsTemplate(
	reqBody map[string]any,
	templateText string,
	data forcedCodexInstructionsTemplateData,
) (bool, error) {
	rendered, err := renderForcedCodexInstructionsTemplate(templateText, data)
	if err != nil {
		return false, err
	}
	if rendered == "" {
		return false, nil
	}

	existing, _ := reqBody["instructions"].(string)
	if strings.TrimSpace(existing) == rendered {
		return false, nil
	}

	reqBody["instructions"] = rendered
	return true, nil
}

func renderForcedCodexInstructionsTemplate(
	templateText string,
	data forcedCodexInstructionsTemplateData,
) (string, error) {
	tmpl, err := template.New("forced_codex_instructions").Option("missingkey=zero").Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("parse forced codex instructions template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render forced codex instructions template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

func applyOpenAICompatKnowledgeCutoffGuardToResponsesBody(
	body []byte,
	originalModel string,
	upstreamModel string,
) ([]byte, bool, error) {
	if !shouldApplyOpenAICompatKnowledgeCutoffGuard(originalModel, upstreamModel) {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, false, fmt.Errorf("unmarshal for gpt compatibility instructions guard: %w", err)
	}
	if !applyOpenAICompatKnowledgeCutoffGuard(reqBody, originalModel, upstreamModel) {
		return body, false, nil
	}

	updated, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("remarshal after gpt compatibility instructions guard: %w", err)
	}
	return updated, true, nil
}

func shouldApplyOpenAICompatKnowledgeCutoffGuard(originalModel string, upstreamModel string) bool {
	original := normalizeBillingGuardModel(originalModel)
	upstream := normalizeBillingGuardModel(upstreamModel)
	return isClaudeCompatBillingModel(original) && strings.HasPrefix(upstream, "gpt")
}

func applyOpenAICompatKnowledgeCutoffGuard(
	reqBody map[string]any,
	originalModel string,
	upstreamModel string,
) bool {
	if reqBody == nil {
		return false
	}

	existing, _ := reqBody["instructions"].(string)
	existing = strings.TrimSpace(existing)
	if strings.Contains(existing, openAICompatKnowledgeCutoffGuardMarker) {
		return false
	}

	guard := renderOpenAICompatKnowledgeCutoffGuard(originalModel, upstreamModel)
	if guard == "" {
		return false
	}
	if existing != "" {
		reqBody["instructions"] = guard + "\n\n" + existing
	} else {
		reqBody["instructions"] = guard
	}
	return true
}

func renderOpenAICompatKnowledgeCutoffGuard(originalModel string, upstreamModel string) string {
	original := strings.TrimSpace(originalModel)
	upstream := strings.TrimSpace(upstreamModel)
	if upstream == "" {
		return ""
	}
	if original == "" {
		original = "Claude-compatible"
	}
	return fmt.Sprintf(
		"%s The client is using a Claude-compatible /v1/messages protocol model name (%s), but this request is executed by the upstream model %s. When asked about model identity, knowledge cutoff, training cutoff, or built-in knowledge date, use the actual upstream model %s. Do not infer identity or cutoff dates from the Claude-compatible request name. If the exact knowledge cutoff date is unavailable, say it cannot be confirmed instead of inventing a date.",
		openAICompatKnowledgeCutoffGuardMarker,
		original,
		upstream,
		upstream,
	)
}
