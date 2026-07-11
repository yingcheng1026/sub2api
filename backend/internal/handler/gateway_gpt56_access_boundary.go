package handler

import (
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func genericGatewayGPT56AccessError(model string) (int, string, string, bool) {
	normalized, isFamily := service.NormalizeOpenAIGPT56PreviewModel(model)
	if !isFamily {
		return 0, "", "", false
	}
	if normalized == "" {
		return http.StatusBadRequest, "invalid_request_error",
			"GPT-5.6 requires an exact supported tier: gpt-5.6-sol, gpt-5.6-terra, or gpt-5.6-luna", true
	}
	return http.StatusForbidden, "model_not_available",
		fmt.Sprintf("GPT-5.6 model %s is not available for this API key group", normalized), true
}

func genericGatewayGPT56MappedAccessError(mapping service.ChannelMappingResult) (int, string, string, bool) {
	if !mapping.Mapped {
		return 0, "", "", false
	}
	return genericGatewayGPT56AccessError(mapping.MappedModel)
}

func genericGatewayGPT56AccountAccessError(account *service.Account, requestedModel string, mapping service.ChannelMappingResult) (int, string, string, bool) {
	if account == nil {
		return 0, "", "", false
	}
	model := requestedModel
	if mapping.Mapped {
		model = mapping.MappedModel
	}
	mappedModel, matched := account.ResolveMappedModel(model)
	if !matched {
		return 0, "", "", false
	}
	return genericGatewayGPT56AccessError(mappedModel)
}
