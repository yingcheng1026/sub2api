package service

import (
	"context"
	"time"
)

type accountCredentialsUpdater interface {
	UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error
}

func persistAccountCredentials(ctx context.Context, repo AccountRepository, account *Account, credentials map[string]any) error {
	if repo == nil || account == nil {
		return nil
	}

	account.Credentials = cloneCredentials(credentials)
	AdvanceOAuthTokenVersion(account)
	if updater, ok := any(repo).(accountCredentialsUpdater); ok {
		return updater.UpdateCredentials(ctx, account.ID, account.Credentials)
	}
	return repo.Update(ctx, account)
}

// AdvanceOAuthTokenVersion makes every persisted OAuth credential generation
// distinguishable from the previous one. Milliseconds stay exactly representable
// in JSON numbers; the +1 fallback preserves monotonicity during same-ms writes.
func AdvanceOAuthTokenVersion(account *Account) {
	if account == nil || account.Type != AccountTypeOAuth {
		return
	}
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	account.Credentials["_token_version"] = nextOAuthTokenVersion(account.GetCredentialAsInt64("_token_version"))
}

func nextOAuthTokenVersion(current int64) int64 {
	next := time.Now().UnixMilli()
	if next <= current {
		return current + 1
	}
	return next
}

func cloneCredentials(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
