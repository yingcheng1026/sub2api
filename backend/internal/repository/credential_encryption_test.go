package repository

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type fakeCredentialEncryptor struct{}

func (fakeCredentialEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (fakeCredentialEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, "enc:"), nil
}

func TestEncryptAccountCredentialsProtectsSensitiveKeysOnly(t *testing.T) {
	input := map[string]any{
		"api_key":       "sk-secret",
		"access_token":  "at-secret",
		"model_mapping": map[string]any{"gpt-5": "upstream-model"},
		"nested": map[string]any{
			"refresh_token": "rt-secret",
			"region":        "us-east-1",
		},
	}

	stored, err := encryptAccountCredentials(input, fakeCredentialEncryptor{})
	if err != nil {
		t.Fatalf("encryptAccountCredentials() error = %v", err)
	}

	if stored["api_key"] == "sk-secret" {
		t.Fatal("api_key was stored as plaintext")
	}
	if stored["access_token"] == "at-secret" {
		t.Fatal("access_token was stored as plaintext")
	}
	nested, ok := stored["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested credentials = %#v, want map[string]any", stored["nested"])
	}
	if nested["refresh_token"] == "rt-secret" {
		t.Fatal("nested refresh_token was stored as plaintext")
	}
	if got := stored["model_mapping"]; !reflect.DeepEqual(got, input["model_mapping"]) {
		t.Fatalf("model_mapping = %#v, want %#v", got, input["model_mapping"])
	}

	decrypted, err := decryptAccountCredentials(stored, fakeCredentialEncryptor{})
	if err != nil {
		t.Fatalf("decryptAccountCredentials() error = %v", err)
	}
	if !reflect.DeepEqual(decrypted, input) {
		t.Fatalf("decrypted = %#v, want %#v", decrypted, input)
	}
}

func TestEncryptAccountCredentialsIsIdempotentForEncryptedValues(t *testing.T) {
	input := map[string]any{"api_key": "sk-secret"}

	stored, err := encryptAccountCredentials(input, fakeCredentialEncryptor{})
	if err != nil {
		t.Fatalf("first encrypt error = %v", err)
	}
	storedAgain, err := encryptAccountCredentials(stored, fakeCredentialEncryptor{})
	if err != nil {
		t.Fatalf("second encrypt error = %v", err)
	}

	if !reflect.DeepEqual(storedAgain, stored) {
		t.Fatalf("storedAgain = %#v, want %#v", storedAgain, stored)
	}
}

func TestEncryptAccountCredentialsWithoutEncryptorReturnsPlainCopy(t *testing.T) {
	input := map[string]any{"api_key": "sk-secret"}

	stored, err := encryptAccountCredentials(input, nil)
	if err != nil {
		t.Fatalf("encryptAccountCredentials() error = %v", err)
	}
	if !reflect.DeepEqual(stored, input) {
		t.Fatalf("stored = %#v, want %#v", stored, input)
	}
	stored["api_key"] = "changed"
	if input["api_key"] != "sk-secret" {
		t.Fatal("encryptAccountCredentials returned the original map")
	}
}

func TestEncryptPlaintextAccountCredentialsBackfillsOnlyPlaintext(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := fakeCredentialEncryptor{}

	plain, err := client.Account.Create().
		SetName("plain").
		SetPlatform("openai").
		SetType("api_key").
		SetCredentials(map[string]any{
			"api_key":  "sk-secret",
			"base_url": "https://api.example.test",
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create plain account: %v", err)
	}

	alreadyEncryptedCredentials, err := encryptAccountCredentials(map[string]any{"api_key": "already-encrypted"}, encryptor)
	if err != nil {
		t.Fatalf("encrypt fixture credentials: %v", err)
	}
	alreadyEncrypted, err := client.Account.Create().
		SetName("encrypted").
		SetPlatform("openai").
		SetType("api_key").
		SetCredentials(alreadyEncryptedCredentials).
		Save(ctx)
	if err != nil {
		t.Fatalf("create encrypted account: %v", err)
	}

	dryRun, err := EncryptPlaintextAccountCredentials(ctx, client, encryptor, true)
	if err != nil {
		t.Fatalf("dry run backfill: %v", err)
	}
	if dryRun.Scanned != 2 || dryRun.NeedsEncryption != 1 || dryRun.Updated != 0 {
		t.Fatalf("dry run result = %#v", dryRun)
	}

	result, err := EncryptPlaintextAccountCredentials(ctx, client, encryptor, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if result.Scanned != 2 || result.NeedsEncryption != 1 || result.Updated != 1 {
		t.Fatalf("backfill result = %#v", result)
	}

	refreshedPlain, err := client.Account.Get(ctx, plain.ID)
	if err != nil {
		t.Fatalf("get plain account: %v", err)
	}
	if refreshedPlain.Credentials["api_key"] == "sk-secret" {
		t.Fatal("plaintext credential was not encrypted")
	}
	decrypted, err := decryptAccountCredentials(refreshedPlain.Credentials, encryptor)
	if err != nil {
		t.Fatalf("decrypt backfilled credentials: %v", err)
	}
	if decrypted["api_key"] != "sk-secret" {
		t.Fatalf("decrypted api_key = %#v", decrypted["api_key"])
	}

	refreshedEncrypted, err := client.Account.Get(ctx, alreadyEncrypted.ID)
	if err != nil {
		t.Fatalf("get encrypted account: %v", err)
	}
	if !reflect.DeepEqual(refreshedEncrypted.Credentials, alreadyEncryptedCredentials) {
		t.Fatalf("already encrypted credentials changed: %#v", refreshedEncrypted.Credentials)
	}
}

func newCredentialEncryptionTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:credential_encryption?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}
