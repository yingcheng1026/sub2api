package service

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type channelMonitorPayloadTestEncryptor struct{}

func (channelMonitorPayloadTestEncryptor) Encrypt(plaintext string) (string, error) {
	return "legacy:" + base64.RawURLEncoding.EncodeToString([]byte(plaintext)), nil
}

func (channelMonitorPayloadTestEncryptor) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "legacy:") {
		return "", errors.New("invalid legacy ciphertext")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ciphertext, "legacy:"))
	return string(raw), err
}

func (channelMonitorPayloadTestEncryptor) EncryptForDomain(domain, plaintext string) (string, error) {
	raw := domain + "\x00" + plaintext
	return "domain:" + base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

func (channelMonitorPayloadTestEncryptor) DecryptForDomain(domain, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "domain:") {
		return "", errors.New("invalid domain ciphertext")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ciphertext, "domain:"))
	if err != nil {
		return "", err
	}
	prefix := domain + "\x00"
	if !strings.HasPrefix(string(raw), prefix) {
		return "", errors.New("secret domain mismatch")
	}
	return strings.TrimPrefix(string(raw), prefix), nil
}

func TestChannelMonitorSecretPayloadRoundTripAndStrictFailures(t *testing.T) {
	encryptor := channelMonitorPayloadTestEncryptor{}
	headers := map[string]string{"User-Agent": "monitor-test", "anthropic-beta": "feature"}
	body := map[string]any{"metadata": map[string]any{"token": "body-secret"}, "temperature": 0.25}

	sealedHeaders, err := SealChannelMonitorExtraHeaders(encryptor, headers)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealedHeaders) != 1 || sealedHeaders[ChannelMonitorSecretEnvelopeKey] == "" {
		t.Fatalf("sealed headers = %#v", sealedHeaders)
	}
	openedHeaders, err := OpenChannelMonitorExtraHeaders(encryptor, sealedHeaders)
	if err != nil || !reflect.DeepEqual(openedHeaders, headers) {
		t.Fatalf("opened headers = %#v, err=%v", openedHeaders, err)
	}

	sealedBody, err := SealChannelMonitorBodyOverride(encryptor, body)
	if err != nil {
		t.Fatal(err)
	}
	openedBody, err := OpenChannelMonitorBodyOverride(encryptor, sealedBody)
	if err != nil || !reflect.DeepEqual(openedBody, body) {
		t.Fatalf("opened body = %#v, err=%v", openedBody, err)
	}

	if _, err := OpenChannelMonitorExtraHeaders(encryptor, headers); err == nil {
		t.Fatal("plaintext headers were accepted")
	}
	malformedHeaders := map[string]string{
		ChannelMonitorSecretEnvelopeKey: sealedHeaders[ChannelMonitorSecretEnvelopeKey],
		"User-Agent":                    "plaintext-sibling",
	}
	if _, err := OpenChannelMonitorExtraHeaders(encryptor, malformedHeaders); err == nil {
		t.Fatal("marker envelope with plaintext sibling was accepted")
	}
	wrongDomain, err := encryptor.EncryptForDomain(SecretDomainSettingSecret, `{"version":1,"kind":"extra_headers","headers":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenChannelMonitorExtraHeaders(encryptor, map[string]string{ChannelMonitorSecretEnvelopeKey: wrongDomain}); err == nil {
		t.Fatal("wrong-domain ciphertext was accepted")
	}
	if _, err := OpenChannelMonitorBodyOverride(encryptor, map[string]any{ChannelMonitorSecretEnvelopeKey: sealedHeaders[ChannelMonitorSecretEnvelopeKey]}); err == nil {
		t.Fatal("header payload was accepted as body payload")
	}
	if got, err := OpenChannelMonitorBodyOverride(encryptor, nil); err != nil || got != nil {
		t.Fatalf("nil body = %#v, err=%v", got, err)
	}
}

func TestChannelMonitorCredentialHeadersAndEnvelopeMarkerAreForbidden(t *testing.T) {
	for _, name := range []string{
		"Authorization", "proxy-authorization", "Cookie", "Set-Cookie",
		"x-api-key", "API-Key", "X-Goog-Api-Key", "x-auth-token",
		"X-Amz-Security-Token", "X-Client-Secret", "X-Request-Signature",
		"Private-Token", "Ocp-Apim-Subscription-Key", "X-Password",
		ChannelMonitorSecretEnvelopeKey,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExtraHeaders(map[string]string{name: "attacker-value"}); err == nil {
				t.Fatalf("credential-shaped header %q was accepted", name)
			}
		})
	}
	if err := validateExtraHeaders(map[string]string{"User-Agent": "sub2api-monitor/1", "anthropic-beta": "interleaved-thinking-2025-05-14"}); err != nil {
		t.Fatalf("ordinary headers rejected: %v", err)
	}
	for name, value := range map[string]string{
		"User-Agent":     "claude-cli/2.1.92 (external, cli)",
		"X-App":          "cli",
		"anthropic-beta": "interleaved-thinking-2025-05-14,claude-code-20250219",
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
	} {
		if err := validateExtraHeaders(map[string]string{name: value}); err == nil {
			t.Fatalf("official-client attribution header %q was accepted", name)
		}
	}
	if err := validateBodyModeParams(MonitorBodyOverrideModeMerge, map[string]any{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
	}); err == nil {
		t.Fatal("official-client attribution body was accepted")
	}
	if err := validateBodyModeParams(MonitorBodyOverrideModeReplace, map[string]any{ChannelMonitorSecretEnvelopeKey: "collision"}); err == nil {
		t.Fatal("reserved body envelope marker was accepted")
	}

	base := map[string]string{"Authorization": "Bearer real", "x-api-key": "real-key"}
	got := mergeHeaders(base, &CheckOptions{ExtraHeaders: map[string]string{
		"authorization": "Bearer attacker",
		"X-API-Key":     "attacker-key",
		"User-Agent":    "sub2api-monitor/1",
	}})
	if got["Authorization"] != "Bearer real" || got["x-api-key"] != "real-key" {
		t.Fatalf("adapter credentials were overridden: %#v", got)
	}
	if got["User-Agent"] != "sub2api-monitor/1" {
		t.Fatalf("ordinary header was not merged: %#v", got)
	}
}

type channelMonitorPayloadRepoSpy struct {
	ChannelMonitorRepository
	created *ChannelMonitor
	updated *ChannelMonitor
	stored  *ChannelMonitor
}

func (r *channelMonitorPayloadRepoSpy) Create(_ context.Context, m *ChannelMonitor) error {
	r.created = cloneChannelMonitorPayloadTest(m)
	m.ID = 41
	r.created.ID = m.ID
	return nil
}

func (r *channelMonitorPayloadRepoSpy) GetByID(_ context.Context, _ int64) (*ChannelMonitor, error) {
	return cloneChannelMonitorPayloadTest(r.stored), nil
}

func (r *channelMonitorPayloadRepoSpy) Update(_ context.Context, m *ChannelMonitor) error {
	r.updated = cloneChannelMonitorPayloadTest(m)
	return nil
}

func TestChannelMonitorServicePersistsEncryptedPayloadAndReturnsPlaintext(t *testing.T) {
	encryptor := channelMonitorPayloadTestEncryptor{}
	repo := &channelMonitorPayloadRepoSpy{}
	svc := NewChannelMonitorService(repo, encryptor)
	wantHeaders := map[string]string{"User-Agent": "monitor-client"}
	wantBody := map[string]any{"metadata": map[string]any{"token": "body-secret"}}

	created, err := svc.Create(context.Background(), ChannelMonitorCreateParams{
		Name:             "encrypted-monitor",
		Provider:         MonitorProviderOpenAI,
		Endpoint:         "https://api.example.test",
		APIKey:           "api-secret",
		PrimaryModel:     "gpt-test",
		Enabled:          true,
		IntervalSeconds:  60,
		CreatedBy:        1,
		ExtraHeaders:     wantHeaders,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     wantBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.created == nil || !HasChannelMonitorExtraHeadersEnvelopeMarker(repo.created.ExtraHeaders) || !HasChannelMonitorBodyOverrideEnvelopeMarker(repo.created.BodyOverride) {
		t.Fatalf("repository received plaintext payload: %#v", repo.created)
	}
	if strings.Contains(repo.created.ExtraHeaders[ChannelMonitorSecretEnvelopeKey], "monitor-client") || strings.Contains(repo.created.BodyOverride[ChannelMonitorSecretEnvelopeKey].(string), "body-secret") {
		t.Fatal("repository payload ciphertext contains plaintext secret")
	}
	if created.APIKey != "api-secret" || !reflect.DeepEqual(created.ExtraHeaders, wantHeaders) || !reflect.DeepEqual(created.BodyOverride, wantBody) {
		t.Fatalf("create response = %#v", created)
	}

	repo.stored = repo.created
	got, err := svc.Get(context.Background(), 41)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "api-secret" || !reflect.DeepEqual(got.ExtraHeaders, wantHeaders) || !reflect.DeepEqual(got.BodyOverride, wantBody) {
		t.Fatalf("get response = %#v", got)
	}
	newName := "updated-monitor"
	repo.stored = repo.created
	updated, err := svc.Update(context.Background(), 41, ChannelMonitorUpdateParams{Name: &newName})
	if err != nil {
		t.Fatal(err)
	}
	if repo.updated == nil || !HasChannelMonitorExtraHeadersEnvelopeMarker(repo.updated.ExtraHeaders) || !HasChannelMonitorBodyOverrideEnvelopeMarker(repo.updated.BodyOverride) {
		t.Fatalf("repository received plaintext update: %#v", repo.updated)
	}
	if updated.Name != newName || updated.APIKey != "api-secret" || !reflect.DeepEqual(updated.ExtraHeaders, wantHeaders) || !reflect.DeepEqual(updated.BodyOverride, wantBody) {
		t.Fatalf("update response = %#v", updated)
	}

	repo.stored = cloneChannelMonitorPayloadTest(repo.created)
	repo.stored.ExtraHeaders = map[string]string{"User-Agent": "legacy-plaintext"}
	if _, err := svc.Get(context.Background(), 41); err == nil {
		t.Fatal("service accepted a plaintext stored payload")
	}
}

type channelMonitorTemplatePayloadRepoSpy struct {
	ChannelMonitorRequestTemplateRepository
	created    *ChannelMonitorRequestTemplate
	updated    *ChannelMonitorRequestTemplate
	stored     *ChannelMonitorRequestTemplate
	applyCalls int
}

func (r *channelMonitorTemplatePayloadRepoSpy) Create(_ context.Context, t *ChannelMonitorRequestTemplate) error {
	r.created = cloneChannelMonitorTemplatePayloadTest(t)
	t.ID = 72
	r.created.ID = t.ID
	return nil
}

func (r *channelMonitorTemplatePayloadRepoSpy) GetByID(_ context.Context, _ int64) (*ChannelMonitorRequestTemplate, error) {
	return cloneChannelMonitorTemplatePayloadTest(r.stored), nil
}

func (r *channelMonitorTemplatePayloadRepoSpy) Update(_ context.Context, t *ChannelMonitorRequestTemplate) error {
	r.updated = cloneChannelMonitorTemplatePayloadTest(t)
	return nil
}

func (r *channelMonitorTemplatePayloadRepoSpy) ApplyToMonitors(_ context.Context, _ int64, _ []int64) (int64, error) {
	r.applyCalls++
	return 1, nil
}

func TestChannelMonitorTemplateServiceEncryptsAndBlocksPlaintextFanout(t *testing.T) {
	encryptor := channelMonitorPayloadTestEncryptor{}
	repo := &channelMonitorTemplatePayloadRepoSpy{}
	svc := NewChannelMonitorRequestTemplateService(repo, encryptor)
	wantHeaders := map[string]string{"User-Agent": "template-client"}
	wantBody := map[string]any{"metadata": map[string]any{"token": "template-secret"}}

	created, err := svc.Create(context.Background(), ChannelMonitorRequestTemplateCreateParams{
		Name:             "encrypted-template",
		Provider:         MonitorProviderOpenAI,
		ExtraHeaders:     wantHeaders,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     wantBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.created == nil || !HasChannelMonitorExtraHeadersEnvelopeMarker(repo.created.ExtraHeaders) || !HasChannelMonitorBodyOverrideEnvelopeMarker(repo.created.BodyOverride) {
		t.Fatalf("repository received plaintext template: %#v", repo.created)
	}
	if !reflect.DeepEqual(created.ExtraHeaders, wantHeaders) || !reflect.DeepEqual(created.BodyOverride, wantBody) {
		t.Fatalf("template response = %#v", created)
	}
	description := "updated description"
	repo.stored = repo.created
	updated, err := svc.Update(context.Background(), 72, ChannelMonitorRequestTemplateUpdateParams{Description: &description})
	if err != nil {
		t.Fatal(err)
	}
	if repo.updated == nil || !HasChannelMonitorExtraHeadersEnvelopeMarker(repo.updated.ExtraHeaders) || !HasChannelMonitorBodyOverrideEnvelopeMarker(repo.updated.BodyOverride) {
		t.Fatalf("repository received plaintext template update: %#v", repo.updated)
	}
	if updated.Description != description || !reflect.DeepEqual(updated.ExtraHeaders, wantHeaders) || !reflect.DeepEqual(updated.BodyOverride, wantBody) {
		t.Fatalf("template update response = %#v", updated)
	}

	repo.stored = repo.created
	if _, err := svc.ApplyToMonitors(context.Background(), 72, []int64{41}); err != nil {
		t.Fatal(err)
	}
	if repo.applyCalls != 1 {
		t.Fatalf("apply calls = %d", repo.applyCalls)
	}

	repo.stored = cloneChannelMonitorTemplatePayloadTest(repo.created)
	repo.stored.BodyOverride = map[string]any{"token": "legacy-plaintext"}
	if _, err := svc.ApplyToMonitors(context.Background(), 72, []int64{41}); err == nil {
		t.Fatal("plaintext template was fanned out")
	}
	if repo.applyCalls != 1 {
		t.Fatalf("apply ran after payload validation failed: calls=%d", repo.applyCalls)
	}
}

func cloneChannelMonitorPayloadTest(m *ChannelMonitor) *ChannelMonitor {
	if m == nil {
		return nil
	}
	clone := *m
	clone.ExtraHeaders = cloneStringMap(m.ExtraHeaders)
	clone.BodyOverride = cloneAnyMap(m.BodyOverride)
	return &clone
}

func cloneChannelMonitorTemplatePayloadTest(t *ChannelMonitorRequestTemplate) *ChannelMonitorRequestTemplate {
	if t == nil {
		return nil
	}
	clone := *t
	clone.ExtraHeaders = cloneStringMap(t.ExtraHeaders)
	clone.BodyOverride = cloneAnyMap(t.BodyOverride)
	return &clone
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
