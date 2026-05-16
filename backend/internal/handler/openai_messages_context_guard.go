package handler

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const (
	openAIMessagesContextGuardrailMarker = "[HFC_LONG_CONTEXT_FILE_NAV_GUARD]"

	openAIMessagesGuardrailMinReadToolUses       = 2
	openAIMessagesGuardrailMinReadToolResultSize = 96 * 1024
	openAIMessagesGuardrailMinBodySize           = 512 * 1024
	openAIMessagesRiskLogPromptTokenThreshold    = 100000
)

const openAIMessagesContextGuardrailText = openAIMessagesContextGuardrailMarker + ` Long-context file navigation guardrail: when using file tools, treat line numbers, byte offsets, and character offsets as different coordinate systems. Prefer explicit file paths plus line ranges. Before acting on a Read result, verify the latest returned file path and line range; do not reuse stale offsets from earlier failed reads.`

type openAIMessagesContextRisk struct {
	BodyBytes                  int
	SystemBytes                int
	ToolsBytes                 int
	MessageCount               int
	MessageTextBytes           int
	LargestMessageTextBytes    int
	ToolUseCount               int
	ReadToolUseCount           int
	ToolResultCount            int
	ReadToolResultCount        int
	ToolResultBytes            int
	ReadToolResultBytes        int
	LargestReadToolResultBytes int
	LargestReadFilePath        string
}

func analyzeOpenAIMessagesContextRisk(body []byte) openAIMessagesContextRisk {
	diag := openAIMessagesContextRisk{
		BodyBytes:   len(body),
		SystemBytes: jsonTextBytes(gjson.GetBytes(body, "system")),
		ToolsBytes:  len(gjson.GetBytes(body, "tools").Raw),
	}
	readToolPathsByID := make(map[string]string)

	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		diag.MessageCount++
		content := message.Get("content")
		if content.Type == gjson.String {
			n := len(content.String())
			diag.MessageTextBytes += n
			if n > diag.LargestMessageTextBytes {
				diag.LargestMessageTextBytes = n
			}
			return true
		}
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				n := len(block.Get("text").String())
				diag.MessageTextBytes += n
				if n > diag.LargestMessageTextBytes {
					diag.LargestMessageTextBytes = n
				}
			case "tool_use":
				diag.ToolUseCount++
				if isReadToolName(block.Get("name").String()) {
					diag.ReadToolUseCount++
					if id := strings.TrimSpace(block.Get("id").String()); id != "" {
						readToolPathsByID[id] = strings.TrimSpace(block.Get("input.file_path").String())
					}
				}
			case "tool_result":
				diag.ToolResultCount++
				size := jsonTextBytes(block.Get("content"))
				diag.ToolResultBytes += size
				if path, ok := readToolPathsByID[strings.TrimSpace(block.Get("tool_use_id").String())]; ok {
					diag.ReadToolResultCount++
					diag.ReadToolResultBytes += size
					if size > diag.LargestReadToolResultBytes {
						diag.LargestReadToolResultBytes = size
						diag.LargestReadFilePath = path
					}
				}
			}
			return true
		})
		return true
	})

	return diag
}

func (d openAIMessagesContextRisk) needsFileNavigationGuardrail() bool {
	if d.ReadToolUseCount >= openAIMessagesGuardrailMinReadToolUses && d.ReadToolResultBytes >= openAIMessagesGuardrailMinReadToolResultSize {
		return true
	}
	if d.LargestReadToolResultBytes >= openAIMessagesGuardrailMinReadToolResultSize {
		return true
	}
	if d.BodyBytes >= openAIMessagesGuardrailMinBodySize && d.ReadToolUseCount > 0 {
		return true
	}
	return false
}

func (d openAIMessagesContextRisk) shouldLog(promptTotalTokens int, guardrailInjected bool) bool {
	return guardrailInjected ||
		promptTotalTokens >= openAIMessagesRiskLogPromptTokenThreshold ||
		d.needsFileNavigationGuardrail()
}

func (d openAIMessagesContextRisk) zapFields(promptTotalTokens int, guardrailInjected bool) []zap.Field {
	return []zap.Field{
		zap.Int("prompt_total_tokens", promptTotalTokens),
		zap.Bool("long_context_guardrail_injected", guardrailInjected),
		zap.Int("body_bytes", d.BodyBytes),
		zap.Int("system_bytes", d.SystemBytes),
		zap.Int("tools_bytes", d.ToolsBytes),
		zap.Int("message_count", d.MessageCount),
		zap.Int("message_text_bytes", d.MessageTextBytes),
		zap.Int("largest_message_text_bytes", d.LargestMessageTextBytes),
		zap.Int("tool_use_count", d.ToolUseCount),
		zap.Int("read_tool_use_count", d.ReadToolUseCount),
		zap.Int("tool_result_count", d.ToolResultCount),
		zap.Int("read_tool_result_count", d.ReadToolResultCount),
		zap.Int("tool_result_bytes", d.ToolResultBytes),
		zap.Int("read_tool_result_bytes", d.ReadToolResultBytes),
		zap.Int("largest_read_tool_result_bytes", d.LargestReadToolResultBytes),
		zap.String("largest_read_file_path", d.LargestReadFilePath),
	}
}

func applyOpenAIMessagesContextGuardrail(body []byte, diag openAIMessagesContextRisk) ([]byte, bool, error) {
	if !diag.needsFileNavigationGuardrail() {
		return body, false, nil
	}
	if strings.Contains(string(body), openAIMessagesContextGuardrailMarker) {
		return body, false, nil
	}

	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		updated, err := sjson.SetBytes(body, "system", openAIMessagesContextGuardrailText)
		return updated, err == nil, err
	}

	switch system.Type {
	case gjson.String:
		updated, err := sjson.SetBytes(body, "system", strings.TrimSpace(system.String())+"\n\n"+openAIMessagesContextGuardrailText)
		return updated, err == nil, err
	case gjson.JSON:
		if system.IsArray() {
			updated, err := sjson.SetBytes(body, "system.-1", map[string]string{
				"type": "text",
				"text": openAIMessagesContextGuardrailText,
			})
			return updated, err == nil, err
		}
	}

	return body, false, fmt.Errorf("unsupported anthropic system field for context guardrail: %s", system.Type.String())
}

func jsonTextBytes(v gjson.Result) int {
	switch {
	case !v.Exists():
		return 0
	case v.Type == gjson.String:
		return len(v.String())
	case v.IsArray():
		total := 0
		v.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				total += len(item.String())
				return true
			}
			if text := item.Get("text"); text.Type == gjson.String {
				total += len(text.String())
				return true
			}
			if content := item.Get("content"); content.Exists() {
				total += jsonTextBytes(content)
			}
			return true
		})
		return total
	default:
		return len(v.Raw)
	}
}

func isReadToolName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "read" || normalized == "read_file" || normalized == "readfile"
}
