package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func validateKiroAccountType(accountPlatform, accountType string) error {
	if normalizePlatform(accountPlatform) != PlatformKiro {
		return nil
	}
	if strings.TrimSpace(accountType) == "" || normalizeAccountType(accountType) == AccountTypeAPIKey {
		return nil
	}
	return fmt.Errorf("kiro accounts must use apikey type")
}

func validateKiroAccountGroupIsolation(ctx context.Context, groupRepo GroupRepository, accountPlatform string, groupIDs []int64) error {
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
		if accountPlatform == PlatformKiro || groupPlatform == PlatformKiro {
			if accountPlatform != PlatformKiro || groupPlatform != PlatformKiro {
				return fmt.Errorf(
					"kiro accounts can only be assigned to kiro groups: account platform %s, group %d platform %s",
					accountPlatform,
					groupID,
					groupPlatform,
				)
			}
		}
	}
	return nil
}

func normalizePlatform(platform string) string {
	return strings.ToLower(strings.TrimSpace(platform))
}

func normalizeAccountType(accountType string) string {
	return strings.ToLower(strings.TrimSpace(accountType))
}
