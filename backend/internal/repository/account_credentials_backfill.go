package repository

import (
	"context"
	"errors"
	"reflect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// AccountCredentialsEncryptionResult summarizes a one-shot credentials encryption pass.
type AccountCredentialsEncryptionResult struct {
	Scanned         int
	NeedsEncryption int
	Updated         int
}

// EncryptPlaintextAccountCredentials rewrites legacy plaintext account credentials
// through the same encryption envelope used by accountRepository writes.
func EncryptPlaintextAccountCredentials(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor, dryRun bool) (AccountCredentialsEncryptionResult, error) {
	if client == nil {
		return AccountCredentialsEncryptionResult{}, errors.New("nil ent client")
	}
	if encryptor == nil {
		return AccountCredentialsEncryptionResult{}, errors.New("nil credential encryptor")
	}

	accounts, err := client.Account.Query().
		Where(dbaccount.DeletedAtIsNil()).
		All(ctx)
	if err != nil {
		return AccountCredentialsEncryptionResult{}, err
	}

	result := AccountCredentialsEncryptionResult{Scanned: len(accounts)}
	for _, account := range accounts {
		encrypted, err := encryptAccountCredentials(account.Credentials, encryptor)
		if err != nil {
			return result, err
		}
		if reflect.DeepEqual(encrypted, account.Credentials) {
			continue
		}

		result.NeedsEncryption++
		if dryRun {
			continue
		}
		if _, err := client.Account.UpdateOneID(account.ID).SetCredentials(encrypted).Save(ctx); err != nil {
			return result, err
		}
		result.Updated++
	}

	return result, nil
}
