package service

import (
	"bytes"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ErrAnthropicOAuthGatewayDisabled prevents shared/public gateway traffic from
// using Anthropic OAuth or setup-token credentials. Downstream request fields
// cannot authenticate official Claude Code provenance, so allowing those
// credentials would make provider attribution dependent on attacker-controlled
// headers and body content.
var ErrAnthropicOAuthGatewayDisabled = errors.New("Anthropic OAuth and setup-token credentials are disabled for shared gateway inference; use an Anthropic API key or service account")

func rejectAnthropicOAuthGatewayCredential(account *Account) error {
	if account != nil && account.IsAnthropicOAuthOrSetupToken() {
		return ErrAnthropicOAuthGatewayDisabled
	}
	return nil
}

func isGatewayInferenceCredentialAllowed(account *Account) bool {
	return rejectAnthropicOAuthGatewayCredential(account) == nil
}

const anthropicBillingAttributionPrefix = "x-anthropic-billing-header:"

// stripUntrustedAnthropicBillingAttribution removes provider-attribution
// blocks supplied by an untrusted downstream client. Ordinary system content
// is preserved byte-for-byte.
func stripUntrustedAnthropicBillingAttribution(body []byte) []byte {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return body
	}
	if system.Type == gjson.String {
		if isAnthropicBillingAttributionText(system.String()) {
			if next, err := sjson.DeleteBytes(body, "system"); err == nil {
				return next
			}
		}
		return body
	}
	if !system.IsArray() {
		return body
	}

	kept := make([][]byte, 0, len(system.Array()))
	removed := false
	for _, item := range system.Array() {
		if isAnthropicBillingAttributionText(item.Get("text").String()) {
			removed = true
			continue
		}
		kept = append(kept, []byte(item.Raw))
	}
	if !removed {
		return body
	}
	if len(kept) == 0 {
		if next, err := sjson.DeleteBytes(body, "system"); err == nil {
			return next
		}
		return body
	}

	rawArray := make([]byte, 0, len(system.Raw))
	rawArray = append(rawArray, '[')
	rawArray = append(rawArray, bytes.Join(kept, []byte(","))...)
	rawArray = append(rawArray, ']')
	if next, err := sjson.SetRawBytes(body, "system", rawArray); err == nil {
		return next
	}
	return body
}

func isAnthropicBillingAttributionText(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), anthropicBillingAttributionPrefix)
}
