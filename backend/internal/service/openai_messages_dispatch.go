package service

import "strings"

const (
	defaultOpenAIMessagesDispatchOpusMappedModel   = "gpt-5.5"
	defaultOpenAIMessagesDispatchSonnetMappedModel = "gpt-5.4"
	defaultOpenAIMessagesDispatchHaikuMappedModel  = "gpt-5.4-mini"

	defaultClaudeMessagesDispatchOpusBillingModel   = "claude-opus-4-7"
	defaultClaudeMessagesDispatchSonnetBillingModel = "claude-sonnet-4-6"
	defaultClaudeMessagesDispatchHaikuBillingModel  = "claude-haiku-4-5"
)

func normalizeOpenAIMessagesDispatchMappedModel(model string) string {
	model = NormalizeOpenAICompatRequestedModel(strings.TrimSpace(model))
	return strings.TrimSpace(model)
}

func normalizeOpenAIMessagesDispatchModelConfig(cfg OpenAIMessagesDispatchModelConfig) OpenAIMessagesDispatchModelConfig {
	out := OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:   normalizeOpenAIMessagesDispatchMappedModel(cfg.OpusMappedModel),
		SonnetMappedModel: normalizeOpenAIMessagesDispatchMappedModel(cfg.SonnetMappedModel),
		HaikuMappedModel:  normalizeOpenAIMessagesDispatchMappedModel(cfg.HaikuMappedModel),
	}

	if len(cfg.ExactModelMappings) > 0 {
		out.ExactModelMappings = make(map[string]string, len(cfg.ExactModelMappings))
		for requestedModel, mappedModel := range cfg.ExactModelMappings {
			requestedModel = strings.TrimSpace(requestedModel)
			mappedModel = normalizeOpenAIMessagesDispatchMappedModel(mappedModel)
			if requestedModel == "" || mappedModel == "" {
				continue
			}
			out.ExactModelMappings[requestedModel] = mappedModel
		}
		if len(out.ExactModelMappings) == 0 {
			out.ExactModelMappings = nil
		}
	}

	return out
}

func claudeMessagesDispatchFamily(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return ""
	}
	if strings.Contains(normalized, "/") {
		parts := strings.Split(normalized, "/")
		normalized = strings.TrimSpace(parts[len(parts)-1])
	}

	switch normalized {
	case "opus", "opus[1m]":
		return "opus"
	case "sonnet", "sonnet[1m]", "default":
		return "sonnet"
	case "haiku":
		return "haiku"
	}

	switch {
	case strings.Contains(normalized, "opus"):
		return "opus"
	case strings.Contains(normalized, "sonnet"):
		return "sonnet"
	case strings.Contains(normalized, "haiku"):
		return "haiku"
	default:
		return ""
	}
}

// CanonicalClaudeMessagesDispatchBillingModel maps Claude Code selectors onto
// the Claude product model IDs used by our billing table. The upstream model may
// be GPT, but customer billing stays anchored to the requested Claude product.
func CanonicalClaudeMessagesDispatchBillingModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return ""
	}
	if strings.Contains(normalized, "/") {
		parts := strings.Split(normalized, "/")
		normalized = strings.TrimSpace(parts[len(parts)-1])
	}
	normalized = strings.TrimSuffix(normalized, "[1m]")

	if strings.HasPrefix(normalized, "claude-") {
		return normalized
	}

	switch normalized {
	case "opus":
		return defaultClaudeMessagesDispatchOpusBillingModel
	case "sonnet", "default":
		return defaultClaudeMessagesDispatchSonnetBillingModel
	case "haiku":
		return defaultClaudeMessagesDispatchHaikuBillingModel
	default:
		return ""
	}
}

func (g *Group) ResolveMessagesDispatchModel(requestedModel string) string {
	if g == nil {
		return ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}

	cfg := normalizeOpenAIMessagesDispatchModelConfig(g.MessagesDispatchModelConfig)
	if mappedModel := strings.TrimSpace(cfg.ExactModelMappings[requestedModel]); mappedModel != "" {
		return mappedModel
	}

	switch claudeMessagesDispatchFamily(requestedModel) {
	case "opus":
		if mappedModel := strings.TrimSpace(cfg.OpusMappedModel); mappedModel != "" {
			return mappedModel
		}
		return defaultOpenAIMessagesDispatchOpusMappedModel
	case "sonnet":
		if mappedModel := strings.TrimSpace(cfg.SonnetMappedModel); mappedModel != "" {
			return mappedModel
		}
		return defaultOpenAIMessagesDispatchSonnetMappedModel
	case "haiku":
		if mappedModel := strings.TrimSpace(cfg.HaikuMappedModel); mappedModel != "" {
			return mappedModel
		}
		return defaultOpenAIMessagesDispatchHaikuMappedModel
	default:
		return ""
	}
}

func sanitizeGroupMessagesDispatchFields(g *Group) {
	if g == nil || g.Platform == PlatformOpenAI {
		return
	}
	g.AllowMessagesDispatch = false
	g.DefaultMappedModel = ""
	g.MessagesDispatchModelConfig = OpenAIMessagesDispatchModelConfig{}
}
