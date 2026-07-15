package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var (
	errSidecarStreamUsageMissing = errors.New("sidecar stream completed without usage")
	errSidecarClientDelivery     = errors.New("sidecar stream client delivery failed")
)

type sidecarUsageExtractor func([]byte) service.ClaudeUsage

func sidecarUpstreamContext(requestCtx context.Context, requireMetering bool) context.Context {
	if requestCtx == nil {
		return context.Background()
	}
	if requireMetering {
		return context.WithoutCancel(requestCtx)
	}
	return requestCtx
}

// readMeteredSidecarStream buffers a bounded sidecar stream until a terminal
// usage frame is available. This intentionally trades incremental delivery for
// a fail-closed billing contract: unmetered output is never released to the
// caller, and the retained response is capped by the existing sidecar limit.
func readMeteredSidecarStream(
	reader io.Reader,
	maxBytes int64,
	extract sidecarUsageExtractor,
	requireUsage bool,
) ([]byte, service.ClaudeUsage, error) {
	if reader == nil {
		return nil, service.ClaudeUsage{}, errors.New("sidecar stream body is nil")
	}
	if maxBytes <= 0 {
		return nil, service.ClaudeUsage{}, errors.New("sidecar stream limit must be positive")
	}
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, service.ClaudeUsage{}, err
	}
	if int64(len(body)) > maxBytes {
		return nil, service.ClaudeUsage{}, fmt.Errorf("sidecar stream exceeds %d bytes", maxBytes)
	}
	usage := extractSidecarStreamUsage(body, extract)
	if requireUsage && !sidecarUsagePresent(usage) {
		return nil, service.ClaudeUsage{}, errSidecarStreamUsageMissing
	}
	return body, usage, nil
}

func extractSidecarStreamUsage(body []byte, extract sidecarUsageExtractor) service.ClaudeUsage {
	if extract == nil || len(body) == 0 {
		return service.ClaudeUsage{}
	}
	usage := service.ClaudeUsage{}
	mergeSidecarUsage(&usage, extract(body))

	var eventData [][]byte
	flushEvent := func() {
		if len(eventData) == 0 {
			return
		}
		mergeSidecarUsage(&usage, extract(bytes.Join(eventData, []byte("\n"))))
		eventData = eventData[:0]
	}
	for _, rawLine := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(bytes.TrimSuffix(rawLine, []byte("\r")))
		if len(line) == 0 {
			flushEvent()
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) > 0 && !bytes.Equal(data, []byte("[DONE]")) {
				eventData = append(eventData, append([]byte(nil), data...))
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		mergeSidecarUsage(&usage, extract(line))
	}
	flushEvent()
	return usage
}

func extractCommonSidecarUsage(body []byte) service.ClaudeUsage {
	return service.ClaudeUsage{
		InputTokens: firstKiroInt(body,
			"usage.input_tokens", "usage.prompt_tokens", "usage.inputTokens", "usage.promptTokenCount",
			"message.usage.input_tokens", "message.usage.prompt_tokens",
			"response.usage.input_tokens", "response.usage.prompt_tokens",
		),
		OutputTokens: firstKiroInt(body,
			"usage.output_tokens", "usage.completion_tokens", "usage.outputTokens", "usage.candidatesTokenCount",
			"message.usage.output_tokens", "message.usage.completion_tokens",
			"response.usage.output_tokens", "response.usage.completion_tokens",
		),
		CacheCreationInputTokens: firstKiroInt(body,
			"usage.cache_creation_input_tokens", "usage.cache_creation_tokens",
			"message.usage.cache_creation_input_tokens", "response.usage.cache_creation_input_tokens",
		),
		CacheReadInputTokens: firstKiroInt(body,
			"usage.cache_read_input_tokens", "usage.cache_read_tokens",
			"message.usage.cache_read_input_tokens", "response.usage.cache_read_input_tokens",
		),
	}
}

func mergeSidecarUsage(dst *service.ClaudeUsage, candidate service.ClaudeUsage) {
	if dst == nil {
		return
	}
	dst.InputTokens = maxPositiveInt(dst.InputTokens, candidate.InputTokens)
	dst.OutputTokens = maxPositiveInt(dst.OutputTokens, candidate.OutputTokens)
	dst.CacheCreationInputTokens = maxPositiveInt(dst.CacheCreationInputTokens, candidate.CacheCreationInputTokens)
	dst.CacheReadInputTokens = maxPositiveInt(dst.CacheReadInputTokens, candidate.CacheReadInputTokens)
}

func maxPositiveInt(current, candidate int) int {
	if candidate > current && candidate > 0 {
		return candidate
	}
	return current
}

func sidecarUsagePresent(usage service.ClaudeUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0
}

func writeBufferedSidecarStream(writer io.Writer, body []byte) error {
	if writer == nil {
		return fmt.Errorf("%w: writer is nil", errSidecarClientDelivery)
	}
	if _, err := io.Copy(writer, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("%w: %v", errSidecarClientDelivery, err)
	}
	return nil
}
