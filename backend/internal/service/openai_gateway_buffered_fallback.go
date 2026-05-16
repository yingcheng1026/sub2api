package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func isResponsesTerminalEventType(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func synthesizeBufferedResponsesResponse(acc *apicompat.BufferedResponseAccumulator, requestID, model string) *apicompat.ResponsesResponse {
	if acc == nil || !acc.HasContent() || acc.HasFunctionCalls() {
		return nil
	}

	id := strings.TrimSpace(requestID)
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}

	return &apicompat.ResponsesResponse{
		ID:     id,
		Object: "response",
		Model:  model,
		Status: "completed",
		Output: acc.BuildOutput(),
	}
}

func bufferedMissingTerminalDetail(component string, eventsSeen int, lastEventType string, terminalEventSeen, terminalResponsePresent bool, acc *apicompat.BufferedResponseAccumulator, scanErr error) string {
	scannerErr := ""
	if scanErr != nil {
		scannerErr = scanErr.Error()
	}
	hasContent := acc != nil && acc.HasContent()
	hasFunctionCalls := acc != nil && acc.HasFunctionCalls()
	return fmt.Sprintf(
		"%s upstream stream ended without terminal response payload: events_seen=%d last_event_type=%q terminal_event_seen=%t terminal_response_present=%t accumulated_content=%t accumulated_function_calls=%t scanner_err=%q",
		component,
		eventsSeen,
		lastEventType,
		terminalEventSeen,
		terminalResponsePresent,
		hasContent,
		hasFunctionCalls,
		scannerErr,
	)
}

func newBufferedMissingTerminalFailover(resp *http.Response, detail string) *UpstreamFailoverError {
	headers := http.Header(nil)
	if resp != nil && resp.Header != nil {
		headers = resp.Header.Clone()
	}
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           []byte(detail),
		ResponseHeaders:        headers,
		RetryableOnSameAccount: true,
	}
}
