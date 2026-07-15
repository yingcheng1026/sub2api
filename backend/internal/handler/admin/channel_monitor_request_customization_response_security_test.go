package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type channelMonitorTemplateResolverStub struct {
	tpl *service.ChannelMonitorRequestTemplate
	err error
}

func (s channelMonitorTemplateResolverStub) Get(context.Context, int64) (*service.ChannelMonitorRequestTemplate, error) {
	return s.tpl, s.err
}

func TestChannelMonitorResponseOmitsWriteOnlyRequestCustomization(t *testing.T) {
	m := &service.ChannelMonitor{
		ID:               41,
		APIKey:           "sk-monitor-secret",
		ExtraHeaders:     map[string]string{"X-Client-Trace": "header-private-value"},
		BodyOverrideMode: service.MonitorBodyOverrideModeMerge,
		BodyOverride:     map[string]any{"nested": map[string]any{"token": "body-private-value"}},
		CreatedAt:        time.Unix(1, 0),
		UpdatedAt:        time.Unix(2, 0),
	}

	resp := channelMonitorToResponse(m)
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "header-private-value")
	require.NotContains(t, string(raw), "body-private-value")
	require.True(t, resp.ExtraHeadersConfigured)
	require.Equal(t, 1, resp.ExtraHeaderCount)
	require.True(t, resp.BodyOverrideConfigured)
}

func TestChannelMonitorTemplateResponseOmitsWriteOnlyRequestCustomization(t *testing.T) {
	tpl := &service.ChannelMonitorRequestTemplate{
		ID:               72,
		ExtraHeaders:     map[string]string{"X-Template": "template-header-secret"},
		BodyOverrideMode: service.MonitorBodyOverrideModeReplace,
		BodyOverride:     map[string]any{"token": "template-body-secret"},
		CreatedAt:        time.Unix(3, 0),
		UpdatedAt:        time.Unix(4, 0),
	}

	resp := channelMonitorTemplateToResponse(tpl, 2)
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "template-header-secret")
	require.NotContains(t, string(raw), "template-body-secret")
	require.True(t, resp.ExtraHeadersConfigured)
	require.Equal(t, 1, resp.ExtraHeaderCount)
	require.True(t, resp.BodyOverrideConfigured)
	require.Equal(t, int64(2), resp.AssociatedMonitors)
}

func TestChannelMonitorTemplateSnapshotIsResolvedServerSide(t *testing.T) {
	tpl := &service.ChannelMonitorRequestTemplate{
		ID: 72, Provider: "anthropic",
		ExtraHeaders:     map[string]string{"User-Agent": "monitor-client"},
		BodyOverrideMode: service.MonitorBodyOverrideModeMerge,
		BodyOverride:     map[string]any{"system": "server-side-only"},
	}
	h := &ChannelMonitorHandler{templateService: channelMonitorTemplateResolverStub{tpl: tpl}}
	id := int64(72)
	req := &channelMonitorCreateRequest{Provider: "anthropic", TemplateID: &id}

	headers, mode, body, err := h.resolveCreateRequestCustomization(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, tpl.ExtraHeaders, headers)
	require.Equal(t, tpl.BodyOverrideMode, mode)
	require.Equal(t, tpl.BodyOverride, body)

	req.Provider = "openai"
	_, _, _, err = h.resolveCreateRequestCustomization(context.Background(), req)
	require.ErrorIs(t, err, service.ErrChannelMonitorTemplateProviderMismatch)
}

func TestChannelMonitorUpdateRequiresExplicitWriteOnlyReplacement(t *testing.T) {
	headers := map[string]string{"X-Trace": "private"}
	req := &channelMonitorUpdateRequest{ExtraHeaders: &headers}
	h := &ChannelMonitorHandler{}

	err := h.prepareMonitorCustomizationUpdate(context.Background(), req)
	require.Error(t, err)

	req = &channelMonitorUpdateRequest{ReplaceRequestCustomization: true}
	require.NoError(t, h.prepareMonitorCustomizationUpdate(context.Background(), req))
	require.NotNil(t, req.ExtraHeaders)
	require.Empty(t, *req.ExtraHeaders)
	require.NotNil(t, req.BodyOverrideMode)
	require.Equal(t, service.MonitorBodyOverrideModeOff, *req.BodyOverrideMode)
	require.NotNil(t, req.BodyOverride)
	require.Nil(t, *req.BodyOverride)
	require.True(t, req.ClearTemplate)
}

func TestChannelMonitorTemplateUpdateRequiresExplicitWriteOnlyReplacement(t *testing.T) {
	body := map[string]any{"secret": "private"}
	req := &channelMonitorTemplateUpdateRequest{BodyOverride: &body}
	require.Error(t, prepareTemplateCustomizationUpdate(req))

	req = &channelMonitorTemplateUpdateRequest{ReplaceRequestCustomization: true}
	require.NoError(t, prepareTemplateCustomizationUpdate(req))
	require.NotNil(t, req.ExtraHeaders)
	require.NotNil(t, req.BodyOverrideMode)
	require.NotNil(t, req.BodyOverride)
}
