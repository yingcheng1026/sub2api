package service

import "strings"

// resolveOpenAIForwardModel 解析 OpenAI 兼容转发使用的模型。
// defaultMappedModel 只服务于 /v1/messages 的 Claude 系列显式调度映射，
// 不作为普通 OpenAI 请求的未知模型兜底。
func resolveOpenAIForwardModel(account *Account, requestedModel, defaultMappedModel string) string {
	if account == nil {
		if defaultMappedModel != "" && claudeMessagesDispatchFamily(requestedModel) != "" {
			return defaultMappedModel
		}
		return requestedModel
	}

	mappedModel, matched := account.ResolveMappedModel(requestedModel)
	if !matched && defaultMappedModel != "" && claudeMessagesDispatchFamily(requestedModel) != "" {
		return defaultMappedModel
	}
	return mappedModel
}

// resolveOpenAICompactForwardModel determines the compact-only upstream model
// for /responses/compact requests. It never affects normal /responses traffic.
// When no compact-specific mapping matches, the input model is returned as-is.
func resolveOpenAICompactForwardModel(account *Account, model string) string {
	mappedModel, valid := resolveOpenAICompactForwardModelWithValidity(account, model)
	if !valid {
		return ""
	}
	return mappedModel
}

func resolveOpenAICompactForwardModelWithValidity(account *Account, model string) (string, bool) {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return "", false
	}
	if account == nil {
		return trimmedModel, ValidateOpenAIGPT56ModelTransition(trimmedModel, trimmedModel)
	}

	mappedModel, matched := account.ResolveCompactMappedModel(trimmedModel)
	if !matched {
		return trimmedModel, ValidateOpenAIGPT56ModelTransition(trimmedModel, trimmedModel)
	}
	trimmedMapped := strings.TrimSpace(mappedModel)
	if !ValidateOpenAIGPT56ModelTransition(trimmedModel, trimmedMapped) {
		return "", false
	}
	return trimmedMapped, true
}
