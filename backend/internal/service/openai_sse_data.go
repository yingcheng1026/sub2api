package service

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const openAISSEMaxDataLinesPerEvent = 4096

type openAISSEDataAccumulator struct {
	lines    []string
	bytes    int64
	maxBytes int64
}

func newOpenAISSEDataAccumulator(maxBytes int64) openAISSEDataAccumulator {
	if maxBytes <= 0 {
		maxBytes = defaultUpstreamResponseReadMaxBytes
	}
	return openAISSEDataAccumulator{maxBytes: maxBytes}
}

func (a *openAISSEDataAccumulator) AddLine(line string, fn func([]byte)) error {
	if fn == nil {
		return nil
	}
	trimmedLine := strings.TrimRight(line, "\r\n")
	if data, ok := extractOpenAISSEDataLine(trimmedLine); ok {
		maxBytes := a.maxBytes
		if maxBytes <= 0 {
			maxBytes = defaultUpstreamResponseReadMaxBytes
		}
		separatorBytes := int64(0)
		if len(a.lines) > 0 {
			separatorBytes = 1
		}
		dataBytes := int64(len(data))
		if len(a.lines) >= openAISSEMaxDataLinesPerEvent ||
			dataBytes > maxBytes-a.bytes-separatorBytes {
			return fmt.Errorf("%w: SSE event limit=%d", ErrUpstreamResponseBodyTooLarge, maxBytes)
		}
		a.lines = append(a.lines, data)
		a.bytes += separatorBytes + dataBytes
		return nil
	}
	if strings.TrimSpace(trimmedLine) == "" {
		return a.Flush(fn)
	}
	return nil
}

func (a *openAISSEDataAccumulator) Flush(fn func([]byte)) error {
	if fn == nil || len(a.lines) == 0 {
		return nil
	}
	emitOpenAISSEDataPayloads(a.lines, fn)
	a.lines = a.lines[:0]
	a.bytes = 0
	return nil
}

func readOpenAISSELineBounded(reader *bufio.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("SSE reader is nil")
	}
	if maxBytes <= 0 {
		maxBytes = defaultUpstreamResponseReadMaxBytes
	}

	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if int64(len(chunk)) > maxBytes-int64(len(line)) {
			return nil, fmt.Errorf("%w: SSE line limit=%d", ErrUpstreamResponseBodyTooLarge, maxBytes)
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func forEachOpenAISSEDataPayload(body string, fn func([]byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	acc := newOpenAISSEDataAccumulator(defaultUpstreamResponseReadMaxBytes)
	for _, line := range strings.Split(body, "\n") {
		if err := acc.AddLine(line, fn); err != nil {
			return
		}
	}
	_ = acc.Flush(fn)
}

func emitOpenAISSEDataPayloads(lines []string, fn func([]byte)) {
	if fn == nil || len(lines) == 0 {
		return
	}
	if len(lines) == 1 {
		emitOpenAISSEDataPayload(lines[0], fn)
		return
	}
	joined := strings.Join(lines, "\n")
	if gjson.Valid(joined) {
		emitOpenAISSEDataPayload(joined, fn)
		return
	}
	for _, line := range lines {
		emitOpenAISSEDataPayload(line, fn)
	}
}

func emitOpenAISSEDataPayload(data string, fn func([]byte)) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	fn([]byte(data))
}
