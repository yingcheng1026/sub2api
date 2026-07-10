package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func NormalizeOpenAICompatRequestedModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}

	normalized, _, ok := splitOpenAICompatReasoningModel(trimmed)
	if !ok || normalized == "" {
		return trimmed
	}
	return normalized
}

func applyOpenAICompatModelNormalization(req *apicompat.AnthropicRequest) {
	if req == nil {
		return
	}

	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return
	}

	normalizedModel, derivedEffort, hasReasoningSuffix := splitOpenAICompatReasoningModel(originalModel)
	if hasReasoningSuffix && normalizedModel != "" {
		req.Model = normalizedModel
	}

	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		return
	}

	claudeEffort := openAIReasoningEffortToClaudeOutputEffort(derivedEffort)
	if claudeEffort == "" {
		return
	}

	if req.OutputConfig == nil {
		req.OutputConfig = &apicompat.AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = claudeEffort
}

func splitOpenAICompatReasoningModel(model string) (normalizedModel string, reasoningEffort string, ok bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", "", false
	}

	modelID := trimmed
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	modelID = strings.TrimSpace(modelID)
	if !strings.HasPrefix(strings.ToLower(modelID), "gpt-") {
		return trimmed, "", false
	}

	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return trimmed, "", false
	}

	last := strings.NewReplacer("-", "", "_", "", " ", "").Replace(parts[len(parts)-1])
	_, isGPT56Family := classifyOpenAIGPT56PreviewModel(trimmed)
	switch last {
	case "none":
		reasoningEffort = "none"
	case "minimal":
		if isGPT56Family {
			return trimmed, "", false
		}
	case "low", "medium", "high":
		reasoningEffort = last
	case "xhigh":
		reasoningEffort = "xhigh"
	case "extrahigh":
		if isGPT56Family {
			return trimmed, "", false
		}
		reasoningEffort = "xhigh"
	default:
		return trimmed, "", false
	}

	return normalizeCodexModel(modelID), reasoningEffort, true
}

func openAIGPT56ReasoningEffortFromModel(model string) (string, bool) {
	normalized, isFamily := classifyOpenAIGPT56PreviewModel(model)
	if !isFamily || normalized == "" {
		return "", false
	}
	canonical := canonicalizeOpenAIModelAliasSpelling(model)
	prefix := normalized + "-"
	if !strings.HasPrefix(canonical, prefix) {
		return "", false
	}
	switch effort := strings.TrimPrefix(canonical, prefix); effort {
	case "none", "low", "medium", "high", "xhigh":
		return effort, true
	default:
		return "", false
	}
}

func injectOpenAIGPT56ReasoningEffort(body []byte, model, field string) ([]byte, bool, error) {
	if len(body) == 0 || strings.TrimSpace(field) == "" {
		return body, false, nil
	}
	if gjson.GetBytes(body, "reasoning.effort").Exists() || gjson.GetBytes(body, "reasoning_effort").Exists() {
		return body, false, nil
	}
	effort, ok := openAIGPT56ReasoningEffortFromModel(model)
	if !ok {
		return body, false, nil
	}
	updated, err := sjson.SetBytes(body, field, effort)
	if err != nil {
		return body, false, err
	}
	return updated, true, nil
}

func injectOpenAIGPT56ReasoningEffortMap(reqBody map[string]any, model string) bool {
	if reqBody == nil {
		return false
	}
	if _, present := getOpenAIReasoningEffortFromReqBody(reqBody); present {
		return false
	}
	effort, ok := openAIGPT56ReasoningEffortFromModel(model)
	if !ok {
		return false
	}
	reasoning, ok := reqBody["reasoning"].(map[string]any)
	if !ok || reasoning == nil {
		reasoning = make(map[string]any)
		reqBody["reasoning"] = reasoning
	}
	reasoning["effort"] = effort
	return true
}

func openAIReasoningEffortToClaudeOutputEffort(effort string) string {
	switch strings.TrimSpace(effort) {
	case "none":
		// Anthropic's public output_config does not document "none", but this
		// request is converted locally to OpenAI Responses. Keeping the value
		// in the compatibility struct prevents AnthropicToResponses from
		// replacing an explicit GPT-5.6 "-none" suffix with its medium default.
		return "none"
	case "low", "medium", "high":
		return effort
	case "xhigh":
		return "max"
	default:
		return ""
	}
}
