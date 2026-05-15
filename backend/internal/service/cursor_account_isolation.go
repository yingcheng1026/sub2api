package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const DefaultCursorTestModel = "cursor-default"

type CursorModel struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

var DefaultCursorModels = []CursorModel{
	{ID: DefaultCursorTestModel, Type: "model", DisplayName: "Cursor Default", CreatedAt: ""},
	{ID: "cursor-composer-2-fast", Type: "model", DisplayName: "Cursor Composer 2 Fast", CreatedAt: ""},
	{ID: "cursor-composer-2", Type: "model", DisplayName: "Cursor Composer 2", CreatedAt: ""},
	{ID: "cursor-gpt-5.5-none", Type: "model", DisplayName: "Cursor GPT-5.5 None", CreatedAt: ""},
	{ID: "cursor-gpt-5.5-low", Type: "model", DisplayName: "Cursor GPT-5.5 Low", CreatedAt: ""},
	{ID: "cursor-gpt-5.5-medium", Type: "model", DisplayName: "Cursor GPT-5.5 Medium", CreatedAt: ""},
	{ID: "cursor-gpt-5.5-high", Type: "model", DisplayName: "Cursor GPT-5.5 High", CreatedAt: ""},
	{ID: "cursor-gpt-5.5-extra-high", Type: "model", DisplayName: "Cursor GPT-5.5 Extra High", CreatedAt: ""},
	{ID: "cursor-gpt-5.3-codex", Type: "model", DisplayName: "Cursor GPT-5.3 Codex", CreatedAt: ""},
	{ID: "cursor-gpt-5.3-codex-high", Type: "model", DisplayName: "Cursor GPT-5.3 Codex High", CreatedAt: ""},
	{ID: "cursor-gpt-5.3-codex-xhigh", Type: "model", DisplayName: "Cursor GPT-5.3 Codex XHigh", CreatedAt: ""},
}

func validateCursorAccountType(accountPlatform, accountType string) error {
	if normalizePlatform(accountPlatform) != PlatformCursor {
		return nil
	}
	switch normalizeAccountType(accountType) {
	case "", AccountTypeUpstream:
		return nil
	default:
		return fmt.Errorf("cursor accounts must use upstream type")
	}
}

func validateCursorAccountGroupIsolation(ctx context.Context, groupRepo GroupRepository, accountPlatform string, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	if groupRepo == nil {
		return errors.New("group repository not configured")
	}

	accountPlatform = normalizePlatform(accountPlatform)
	for _, groupID := range groupIDs {
		group, err := groupRepo.GetByIDLite(ctx, groupID)
		if err != nil {
			return fmt.Errorf("get group %d: %w", groupID, err)
		}
		if group == nil {
			return fmt.Errorf("get group %d: %w", groupID, ErrGroupNotFound)
		}
		groupPlatform := normalizePlatform(group.Platform)
		if !isCursorGroupAssignmentCompatible(accountPlatform, groupPlatform) {
			return fmt.Errorf(
				"cursor accounts can only be assigned to cursor groups, and cursor groups only accept cursor accounts: account platform %s, group %d platform %s",
				accountPlatform,
				groupID,
				groupPlatform,
			)
		}
	}
	return nil
}

func isCursorGroupAssignmentCompatible(accountPlatform, groupPlatform string) bool {
	accountPlatform = normalizePlatform(accountPlatform)
	groupPlatform = normalizePlatform(groupPlatform)

	if accountPlatform == PlatformCursor {
		return groupPlatform == PlatformCursor
	}
	if groupPlatform == PlatformCursor {
		return accountPlatform == PlatformCursor
	}
	return true
}

func CursorSidecarAccountRef(account *Account) string {
	if account == nil {
		return ""
	}
	for _, value := range []string{
		account.GetCredential("sidecar_account_ref"),
		stringExtra(account.Extra, "sidecar_account_ref"),
		account.GetCredential("cursor_account_ref"),
		stringExtra(account.Extra, "cursor_account_ref"),
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return fmt.Sprintf("%d", account.ID)
}

func stringExtra(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	value, _ := extra[key].(string)
	return strings.TrimSpace(value)
}
