package service

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ChannelMonitorSecretEnvelopeKey is the only top-level key persisted in the
// JSONB request-customization fields. Keeping the marker outside the encrypted
// payload lets the startup migration distinguish legacy plaintext rows from
// domain-bound ciphertext without relying on secret-looking field names.
const ChannelMonitorSecretEnvelopeKey = "__sub2api_channel_monitor_secret_v1__"

const (
	channelMonitorSecretPayloadVersion = 1
	channelMonitorHeadersPayloadKind   = "extra_headers"
	channelMonitorBodyPayloadKind      = "body_override"
)

type channelMonitorSecretPayload struct {
	Version int               `json:"version"`
	Kind    string            `json:"kind"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    map[string]any    `json:"body,omitempty"`
}

// SealChannelMonitorExtraHeaders encrypts the complete header map. Individual
// values must never be persisted alongside the envelope in plaintext.
func SealChannelMonitorExtraHeaders(encryptor SecretEncryptor, headers map[string]string) (map[string]string, error) {
	if err := validateExtraHeaders(headers); err != nil {
		return nil, err
	}
	payload := channelMonitorSecretPayload{
		Version: channelMonitorSecretPayloadVersion,
		Kind:    channelMonitorHeadersPayloadKind,
		Headers: emptyHeadersIfNil(headers),
	}
	ciphertext, err := sealChannelMonitorPayload(encryptor, payload)
	if err != nil {
		return nil, fmt.Errorf("encrypt channel monitor extra headers: %w", err)
	}
	return map[string]string{ChannelMonitorSecretEnvelopeKey: ciphertext}, nil
}

// OpenChannelMonitorExtraHeaders strictly accepts the marker-only encrypted
// representation. Legacy plaintext, malformed envelopes, and wrong-domain
// ciphertext all fail closed.
func OpenChannelMonitorExtraHeaders(encryptor SecretEncryptor, stored map[string]string) (map[string]string, error) {
	if len(stored) != 1 {
		return nil, errors.New("channel monitor extra headers are not an encrypted envelope")
	}
	ciphertext, ok := stored[ChannelMonitorSecretEnvelopeKey]
	if !ok || ciphertext == "" {
		return nil, errors.New("channel monitor extra headers envelope is malformed")
	}
	payload, err := openChannelMonitorPayload(encryptor, ciphertext, channelMonitorHeadersPayloadKind)
	if err != nil {
		return nil, fmt.Errorf("decrypt channel monitor extra headers: %w", err)
	}
	if payload.Headers == nil {
		payload.Headers = map[string]string{}
	}
	if err := validateExtraHeaders(payload.Headers); err != nil {
		return nil, fmt.Errorf("validate decrypted channel monitor extra headers: %w", err)
	}
	return payload.Headers, nil
}

// SealChannelMonitorBodyOverride encrypts a non-nil complete body map. A SQL
// NULL body contains no secret and remains nil so existing off-mode semantics
// are preserved.
func SealChannelMonitorBodyOverride(encryptor SecretEncryptor, body map[string]any) (map[string]any, error) {
	if body == nil {
		return nil, nil
	}
	if err := validateChannelMonitorBodyOverride(body); err != nil {
		return nil, err
	}
	payload := channelMonitorSecretPayload{
		Version: channelMonitorSecretPayloadVersion,
		Kind:    channelMonitorBodyPayloadKind,
		Body:    body,
	}
	ciphertext, err := sealChannelMonitorPayload(encryptor, payload)
	if err != nil {
		return nil, fmt.Errorf("encrypt channel monitor body override: %w", err)
	}
	return map[string]any{ChannelMonitorSecretEnvelopeKey: ciphertext}, nil
}

// OpenChannelMonitorBodyOverride is the strict inverse of
// SealChannelMonitorBodyOverride. nil is the only unencrypted representation
// accepted because it carries no payload.
func OpenChannelMonitorBodyOverride(encryptor SecretEncryptor, stored map[string]any) (map[string]any, error) {
	if stored == nil {
		return nil, nil
	}
	if len(stored) != 1 {
		return nil, errors.New("channel monitor body override is not an encrypted envelope")
	}
	raw, ok := stored[ChannelMonitorSecretEnvelopeKey]
	if !ok {
		return nil, errors.New("channel monitor body override envelope is malformed")
	}
	ciphertext, ok := raw.(string)
	if !ok || ciphertext == "" {
		return nil, errors.New("channel monitor body override ciphertext is malformed")
	}
	payload, err := openChannelMonitorPayload(encryptor, ciphertext, channelMonitorBodyPayloadKind)
	if err != nil {
		return nil, fmt.Errorf("decrypt channel monitor body override: %w", err)
	}
	if payload.Body == nil {
		payload.Body = map[string]any{}
	}
	if err := validateChannelMonitorBodyOverride(payload.Body); err != nil {
		return nil, fmt.Errorf("validate decrypted channel monitor body override: %w", err)
	}
	return payload.Body, nil
}

// HasChannelMonitorExtraHeadersEnvelopeMarker reports marker presence, not
// validity. The migration uses this to ensure malformed marker-shaped rows are
// rejected instead of being re-encrypted as if they were plaintext.
func HasChannelMonitorExtraHeadersEnvelopeMarker(headers map[string]string) bool {
	_, ok := headers[ChannelMonitorSecretEnvelopeKey]
	return ok
}

// HasChannelMonitorBodyOverrideEnvelopeMarker is the body-map counterpart of
// HasChannelMonitorExtraHeadersEnvelopeMarker.
func HasChannelMonitorBodyOverrideEnvelopeMarker(body map[string]any) bool {
	_, ok := body[ChannelMonitorSecretEnvelopeKey]
	return ok
}

func sealChannelMonitorPayload(encryptor SecretEncryptor, payload channelMonitorSecretPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return EncryptForSecretDomain(encryptor, SecretDomainChannelMonitor, string(raw))
}

func openChannelMonitorPayload(encryptor SecretEncryptor, ciphertext, wantKind string) (channelMonitorSecretPayload, error) {
	plain, err := DecryptForSecretDomain(encryptor, SecretDomainChannelMonitor, ciphertext)
	if err != nil {
		return channelMonitorSecretPayload{}, err
	}
	var payload channelMonitorSecretPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		return channelMonitorSecretPayload{}, err
	}
	if payload.Version != channelMonitorSecretPayloadVersion || payload.Kind != wantKind {
		return channelMonitorSecretPayload{}, errors.New("channel monitor secret payload type mismatch")
	}
	return payload, nil
}

func validateChannelMonitorBodyEnvelopeKey(body map[string]any) error {
	if _, exists := body[ChannelMonitorSecretEnvelopeKey]; exists {
		return ErrChannelMonitorTemplateBodyReservedKey
	}
	return nil
}
