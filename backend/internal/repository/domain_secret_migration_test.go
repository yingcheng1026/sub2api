package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestMigrateDomainBoundSecretsCoversPersistentSecretStores(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	domainEncryptor := encryptor.(service.DomainSecretEncryptor)
	aesEncryptor := encryptor.(*AESEncryptor)
	v2AccountCiphertext := mustV2DomainEncrypt(
		t,
		aesEncryptor,
		service.SecretDomainAccountCredential,
		`"account-secret"`,
	)
	account, err := client.Account.Create().
		SetName("v2-domain-account").
		SetPlatform("openai").
		SetType("api_key").
		SetCredentials(map[string]any{
			"api_key": encryptedCredentialEnvelope(encryptedCredentialAlgV2, v2AccountCiphertext),
		}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	legacyTOTP := mustLegacyEncrypt(t, encryptor, "JBSWY3DPEHPK3PXP")
	user, err := client.User.Create().
		SetEmail("domain-migration@example.test").
		SetPasswordHash("password-hash").
		SetTotpSecretEncrypted(legacyTOTP).
		SetTotpEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	softDeletedUser, err := client.User.Create().
		SetEmail("domain-migration-deleted@example.test").
		SetPasswordHash("password-hash").
		SetTotpSecretEncrypted(legacyTOTP).
		SetTotpEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.User.UpdateOneID(softDeletedUser.ID).SetDeletedAt(time.Now()).Save(ctx); err != nil {
		t.Fatal(err)
	}

	legacyMonitorKey := mustLegacyEncrypt(t, encryptor, "monitor-secret")
	monitor, err := client.ChannelMonitor.Create().
		SetName("legacy-monitor").
		SetProvider("openai").
		SetEndpoint("https://api.example.test").
		SetAPIKeyEncrypted(legacyMonitorKey).
		SetPrimaryModel("gpt-test").
		SetIntervalSeconds(60).
		SetCreatedBy(1).
		SetExtraHeaders(map[string]string{"User-Agent": "legacy-monitor-client"}).
		SetBodyOverrideMode(service.MonitorBodyOverrideModeReplace).
		SetBodyOverride(map[string]any{"metadata": map[string]any{"token": "monitor-body-secret"}}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	template, err := client.ChannelMonitorRequestTemplate.Create().
		SetName("legacy-template").
		SetProvider("openai").
		SetExtraHeaders(map[string]string{"User-Agent": "legacy-template-client"}).
		SetBodyOverrideMode(service.MonitorBodyOverrideModeReplace).
		SetBodyOverride(map[string]any{"metadata": map[string]any{"token": "template-body-secret"}}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	backup := service.BackupS3Config{
		Endpoint:        "https://s3.example.test",
		Bucket:          "backup",
		AccessKeyID:     "access",
		SecretAccessKey: mustLegacyEncrypt(t, encryptor, "backup-secret"),
	}
	backupJSON, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().SetKey("backup_s3_config").SetValue(string(backupJSON)).Save(ctx); err != nil {
		t.Fatal(err)
	}

	moderation := service.ContentModerationConfig{
		Enabled:          true,
		EncryptedAPIKeys: []string{mustLegacyEncrypt(t, encryptor, "moderation-secret")},
	}
	moderationJSON, err := json.Marshal(moderation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().SetKey(service.SettingKeyContentModerationConfig).SetValue(string(moderationJSON)).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().SetKey(service.SettingKeyAdminAPIKey).SetValue("plaintext-admin-api-key").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().SetKey(service.SettingKeySiteName).SetValue("public-site-name").Save(ctx); err != nil {
		t.Fatal(err)
	}
	proxyRow, err := client.Proxy.Create().
		SetName("legacy-proxy").
		SetProtocol("http").
		SetHost("proxy.example.test").
		SetPort(8080).
		SetPassword("proxy-secret").
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	softDeletedProxy, err := client.Proxy.Create().
		SetName("legacy-deleted-proxy").
		SetProtocol("http").
		SetHost("deleted-proxy.example.test").
		SetPort(8081).
		SetPassword("deleted-proxy-secret").
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Proxy.UpdateOneID(softDeletedProxy.ID).SetDeletedAt(time.Now()).Save(ctx); err != nil {
		t.Fatal(err)
	}
	emptyProxy, err := client.Proxy.Create().
		SetName("legacy-empty-proxy").
		SetProtocol("http").
		SetHost("empty-proxy.example.test").
		SetPort(8082).
		SetPassword("").
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacyProxyCiphertext := mustLegacyEncrypt(t, encryptor, "legacy-encrypted-proxy-secret")
	legacyEncryptedProxy, err := client.Proxy.Create().
		SetName("legacy-encrypted-proxy").
		SetProtocol("http").
		SetHost("legacy-encrypted-proxy.example.test").
		SetPort(8083).
		SetPassword(legacyProxyCiphertext).
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v2ProxyCiphertext := mustV2DomainEncrypt(t, aesEncryptor, service.SecretDomainProxyCredential, "v2-proxy-secret")
	v2Proxy, err := client.Proxy.Create().
		SetName("v2-proxy").
		SetProtocol("http").
		SetHost("v2-proxy.example.test").
		SetPort(8084).
		SetPassword(v2ProxyCiphertext).
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	boundProxyCiphertext, err := domainEncryptor.EncryptForDomain(service.SecretDomainProxyCredential, "already-bound-proxy-secret")
	if err != nil {
		t.Fatal(err)
	}
	boundProxy, err := client.Proxy.Create().
		SetName("already-bound-proxy").
		SetProtocol("http").
		SetHost("already-bound-proxy.example.test").
		SetPort(8085).
		SetPassword(boundProxyCiphertext).
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result, err := MigrateDomainBoundSecrets(ctx, client, encryptor)
	if err != nil {
		t.Fatalf("MigrateDomainBoundSecrets() error = %v", err)
	}
	if result.AccountCredentials != 1 || result.TOTPSecrets != 2 || result.ChannelMonitorKeys != 1 || result.ChannelMonitorPayloads != 2 || result.BackupS3Configs != 1 || result.ContentModerationConfigs != 1 || result.SettingSecrets != 1 || result.ProxyCredentials != 5 {
		t.Fatalf("migration result = %#v", result)
	}
	refreshedAccount, err := client.Account.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	accountEnvelope, ok := refreshedAccount.Credentials["api_key"].(map[string]any)
	if !ok || accountEnvelope[encryptedCredentialAlgKey] != encryptedCredentialAlgV3 {
		t.Fatalf("migrated account envelope = %#v", refreshedAccount.Credentials)
	}
	decryptedAccount, err := decryptAccountCredentials(refreshedAccount.Credentials, encryptor)
	if err != nil || decryptedAccount["api_key"] != "account-secret" {
		t.Fatalf("migrated account secret = %#v, err=%v", decryptedAccount, err)
	}

	refreshedUser, err := client.User.Get(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainTOTP, *refreshedUser.TotpSecretEncrypted); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("migrated TOTP = %q, err=%v", got, err)
	}
	if _, err := domainEncryptor.DecryptForDomain(service.SecretDomainContentModeration, *refreshedUser.TotpSecretEncrypted); err == nil {
		t.Fatal("TOTP ciphertext decrypted in content moderation domain")
	}
	refreshedSoftDeletedUser, err := client.User.Get(mixins.SkipSoftDelete(ctx), softDeletedUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainTOTP, *refreshedSoftDeletedUser.TotpSecretEncrypted); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("migrated soft-deleted TOTP = %q, err=%v", got, err)
	}

	refreshedMonitor, err := client.ChannelMonitor.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainChannelMonitor, refreshedMonitor.APIKeyEncrypted); err != nil || got != "monitor-secret" {
		t.Fatalf("migrated monitor key = %q, err=%v", got, err)
	}
	monitorHeaders, err := service.OpenChannelMonitorExtraHeaders(encryptor, refreshedMonitor.ExtraHeaders)
	if err != nil || monitorHeaders["User-Agent"] != "legacy-monitor-client" {
		t.Fatalf("migrated monitor headers = %#v, err=%v", monitorHeaders, err)
	}
	monitorBody, err := service.OpenChannelMonitorBodyOverride(encryptor, refreshedMonitor.BodyOverride)
	if err != nil || monitorBody["metadata"].(map[string]any)["token"] != "monitor-body-secret" {
		t.Fatalf("migrated monitor body = %#v, err=%v", monitorBody, err)
	}
	refreshedTemplate, err := client.ChannelMonitorRequestTemplate.Get(ctx, template.ID)
	if err != nil {
		t.Fatal(err)
	}
	templateHeaders, err := service.OpenChannelMonitorExtraHeaders(encryptor, refreshedTemplate.ExtraHeaders)
	if err != nil || templateHeaders["User-Agent"] != "legacy-template-client" {
		t.Fatalf("migrated template headers = %#v, err=%v", templateHeaders, err)
	}
	templateBody, err := service.OpenChannelMonitorBodyOverride(encryptor, refreshedTemplate.BodyOverride)
	if err != nil || templateBody["metadata"].(map[string]any)["token"] != "template-body-secret" {
		t.Fatalf("migrated template body = %#v, err=%v", templateBody, err)
	}

	backupSetting, err := client.Setting.Query().Where(setting.KeyEQ("backup_s3_config")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var migratedBackup service.BackupS3Config
	if err := json.Unmarshal([]byte(backupSetting.Value), &migratedBackup); err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainBackupS3, migratedBackup.SecretAccessKey); err != nil || got != "backup-secret" {
		t.Fatalf("migrated backup secret = %q, err=%v", got, err)
	}

	moderationSetting, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyContentModerationConfig)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var migratedModeration service.ContentModerationConfig
	if err := json.Unmarshal([]byte(moderationSetting.Value), &migratedModeration); err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainContentModeration, migratedModeration.EncryptedAPIKeys[0]); err != nil || got != "moderation-secret" {
		t.Fatalf("migrated moderation secret = %q, err=%v", got, err)
	}
	sensitiveSetting, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyAdminAPIKey)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainSettingSecret, sensitiveSetting.Value); err != nil || got != "plaintext-admin-api-key" {
		t.Fatalf("migrated setting secret = %q, err=%v", got, err)
	}
	publicSetting, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeySiteName)).Only(ctx)
	if err != nil || publicSetting.Value != "public-site-name" {
		t.Fatalf("public setting changed = %#v, err=%v", publicSetting, err)
	}
	refreshedProxy, err := client.Proxy.Get(ctx, proxyRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedProxy.Password == nil {
		t.Fatal("migrated proxy password is nil")
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainProxyCredential, *refreshedProxy.Password); err != nil || got != "proxy-secret" {
		t.Fatalf("migrated proxy credential = %q, err=%v", got, err)
	}
	refreshedSoftDeletedProxy, err := client.Proxy.Get(mixins.SkipSoftDelete(ctx), softDeletedProxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedSoftDeletedProxy.Password == nil {
		t.Fatal("migrated soft-deleted proxy password is nil")
	}
	if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainProxyCredential, *refreshedSoftDeletedProxy.Password); err != nil || got != "deleted-proxy-secret" {
		t.Fatalf("migrated soft-deleted proxy credential = %q, err=%v", got, err)
	}
	refreshedEmptyProxy, err := client.Proxy.Get(ctx, emptyProxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedEmptyProxy.Password != nil {
		t.Fatalf("empty proxy credential was not normalized to NULL: %q", *refreshedEmptyProxy.Password)
	}
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{id: legacyEncryptedProxy.ID, want: "legacy-encrypted-proxy-secret"},
		{id: v2Proxy.ID, want: "v2-proxy-secret"},
	} {
		refreshed, err := client.Proxy.Get(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.Password == nil {
			t.Fatalf("migrated proxy %d password is nil", tc.id)
		}
		if got, err := domainEncryptor.DecryptForDomain(service.SecretDomainProxyCredential, *refreshed.Password); err != nil || got != tc.want {
			t.Fatalf("migrated proxy %d credential = %q, err=%v", tc.id, got, err)
		}
	}
	refreshedBoundProxy, err := client.Proxy.Get(ctx, boundProxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedBoundProxy.Password == nil || *refreshedBoundProxy.Password != boundProxyCiphertext {
		t.Fatalf("already-bound proxy ciphertext changed: got=%v", refreshedBoundProxy.Password)
	}

	again, err := MigrateDomainBoundSecrets(ctx, client, encryptor)
	if err != nil {
		t.Fatal(err)
	}
	if again.TOTPSecrets != 0 || again.ChannelMonitorKeys != 0 || again.ChannelMonitorPayloads != 0 || again.BackupS3Configs != 0 || again.ContentModerationConfigs != 0 || again.SettingSecrets != 0 || again.ProxyCredentials != 0 {
		t.Fatalf("idempotent migration result = %#v", again)
	}
}

func TestMigrateDomainBoundSecretsRejectsUnreadableProxyCiphertextAndRollsBack(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	account, err := client.Account.Create().
		SetName("proxy-rollback-account").
		SetPlatform("openai").
		SetType("api_key").
		SetCredentials(map[string]any{"api_key": "plaintext-account-secret"}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Proxy.Create().
		SetName("corrupt-proxy-migration").
		SetProtocol("http").
		SetHost("corrupt-proxy.example.test").
		SetPort(8090).
		SetPassword("sd:v3:not-valid").
		SetStatus(service.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateDomainBoundSecrets(ctx, client, encryptor); err == nil {
		t.Fatal("migration accepted unreadable proxy ciphertext")
	}
	refreshed, err := client.Account.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Credentials["api_key"] != "plaintext-account-secret" {
		t.Fatalf("failed proxy migration did not roll back prior account rewrite: %#v", refreshed.Credentials)
	}
}

func TestMigrateDomainBoundSecretsRejectsUnsafeOrUnreadableMonitorPayloadAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers func(t *testing.T, encryptor service.SecretEncryptor) map[string]string
	}{
		{
			name: "wrong domain envelope",
			headers: func(t *testing.T, encryptor service.SecretEncryptor) map[string]string {
				t.Helper()
				ciphertext, err := encryptor.(service.DomainSecretEncryptor).EncryptForDomain(
					service.SecretDomainSettingSecret,
					`{"version":1,"kind":"extra_headers","headers":{"User-Agent":"client"}}`,
				)
				if err != nil {
					t.Fatal(err)
				}
				return map[string]string{service.ChannelMonitorSecretEnvelopeKey: ciphertext}
			},
		},
		{
			name: "credential header",
			headers: func(_ *testing.T, _ service.SecretEncryptor) map[string]string {
				return map[string]string{"Authorization": "Bearer stored-secret"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newCredentialEncryptionTestClient(t)
			encryptor := newDomainMigrationTestEncryptor(t)
			legacyKey := mustLegacyEncrypt(t, encryptor, "monitor-key-before-rollback")
			monitor, err := client.ChannelMonitor.Create().
				SetName("rollback-monitor").
				SetProvider("openai").
				SetEndpoint("https://api.example.test").
				SetAPIKeyEncrypted(legacyKey).
				SetPrimaryModel("gpt-test").
				SetIntervalSeconds(60).
				SetCreatedBy(1).
				SetExtraHeaders(tc.headers(t, encryptor)).
				Save(ctx)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := MigrateDomainBoundSecrets(ctx, client, encryptor); err == nil {
				t.Fatal("migration accepted unsafe channel monitor request payload")
			}
			refreshed, err := client.ChannelMonitor.Get(ctx, monitor.ID)
			if err != nil {
				t.Fatal(err)
			}
			if refreshed.APIKeyEncrypted != legacyKey {
				t.Fatal("failed payload migration did not roll back the prior API key rewrite")
			}
		})
	}
}

func TestMigrateDomainBoundSecretsRetiresEncryptedOfficialClientAttribution(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	domainEncryptor := encryptor.(service.DomainSecretEncryptor)

	headerCiphertext, err := domainEncryptor.EncryptForDomain(
		service.SecretDomainChannelMonitor,
		`{"version":1,"kind":"extra_headers","headers":{"User-Agent":"claude-cli/2.1.92 (external, cli)","X-App":"cli"}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	bodyCiphertext, err := domainEncryptor.EncryptForDomain(
		service.SecretDomainChannelMonitor,
		`{"version":1,"kind":"body_override","body":{"system":"You are Claude Code, Anthropic's official CLI for Claude."}}`,
	)
	if err != nil {
		t.Fatal(err)
	}

	monitor, err := client.ChannelMonitor.Create().
		SetName("legacy-encrypted-attribution").
		SetProvider("anthropic").
		SetEndpoint("https://api.anthropic.com").
		SetAPIKeyEncrypted(mustLegacyEncrypt(t, encryptor, "monitor-key")).
		SetPrimaryModel("claude-test").
		SetIntervalSeconds(60).
		SetCreatedBy(1).
		SetExtraHeaders(map[string]string{service.ChannelMonitorSecretEnvelopeKey: headerCiphertext}).
		SetBodyOverrideMode(service.MonitorBodyOverrideModeMerge).
		SetBodyOverride(map[string]any{service.ChannelMonitorSecretEnvelopeKey: bodyCiphertext}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateDomainBoundSecrets(ctx, client, encryptor); err != nil {
		t.Fatal(err)
	}

	refreshed, err := client.ChannelMonitor.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := service.OpenChannelMonitorExtraHeaders(encryptor, refreshed.ExtraHeaders)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 0 || refreshed.BodyOverride != nil || refreshed.BodyOverrideMode != service.MonitorBodyOverrideModeOff {
		t.Fatalf("official-client attribution was not retired: headers=%#v body=%#v mode=%q", headers, refreshed.BodyOverride, refreshed.BodyOverrideMode)
	}
}

func TestMigrateDomainBoundSecretsRejectsMalformedSettingCiphertextAndRollsBack(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	if _, err := client.Setting.Create().
		SetKey(service.SettingKeyAdminAPIKey).
		SetValue("plaintext-admin-before-rollback").
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().
		SetKey(service.SettingKeySMTPPassword).
		SetValue("sd:v3:not-valid").
		Save(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateDomainBoundSecrets(ctx, client, encryptor); err == nil {
		t.Fatal("expected malformed setting ciphertext migration failure")
	}
	adminSetting, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyAdminAPIKey)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if adminSetting.Value != "plaintext-admin-before-rollback" {
		t.Fatalf("transaction did not roll back prior setting migration: %q", adminSetting.Value)
	}
}

func TestDomainSecretMigrationPostgresGuards(t *testing.T) {
	t.Run("lock", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("SELECT pg_advisory_xact_lock\\(\\$1\\)").
			WithArgs(domainSecretMigrationAdvisoryLockID).
			WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_xact_lock"}).AddRow(nil))

		if err := acquireDomainSecretMigrationLock(context.Background(), dialect.Postgres, db); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("validate proxy constraint", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectExec("ALTER TABLE proxies VALIDATE CONSTRAINT chk_proxies_password_encrypted_storage").
			WillReturnResult(sqlmock.NewResult(0, 0))

		if err := validateProxyCredentialStorageConstraint(context.Background(), dialect.Postgres, db); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	if err := acquireDomainSecretMigrationLock(context.Background(), dialect.SQLite, nil); err != nil {
		t.Fatalf("non-Postgres lock guard returned error: %v", err)
	}
	if err := validateProxyCredentialStorageConstraint(context.Background(), dialect.SQLite, nil); err != nil {
		t.Fatalf("non-Postgres constraint guard returned error: %v", err)
	}
}

func TestMigrateDomainBoundSecretsRejectsUnreadableLegacyCiphertext(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	legacyAccountCiphertext := mustLegacyEncrypt(t, encryptor, `"account-secret"`)
	account, err := client.Account.Create().
		SetName("rollback-account").
		SetPlatform("openai").
		SetType("api_key").
		SetCredentials(map[string]any{
			"api_key": encryptedCredentialEnvelope(encryptedCredentialAlgV1, legacyAccountCiphertext),
		}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.User.Create().
		SetEmail("corrupt-domain-migration@example.test").
		SetPasswordHash("password-hash").
		SetTotpSecretEncrypted("not-valid-ciphertext").
		SetTotpEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateDomainBoundSecrets(ctx, client, encryptor); err == nil {
		t.Fatal("migration accepted unreadable legacy TOTP ciphertext")
	}
	refreshed, err := client.Account.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := refreshed.Credentials["api_key"].(map[string]any)
	if !ok || envelope[encryptedCredentialAlgKey] != encryptedCredentialAlgV1 {
		t.Fatalf("failed migration did not roll back prior account rewrite: %#v", refreshed.Credentials)
	}
}

func TestMigrateDomainBoundSecretsUpgradesV2SharedRootCiphertext(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	aesEncryptor := encryptor.(*AESEncryptor)
	v2Ciphertext := mustV2DomainEncrypt(t, aesEncryptor, service.SecretDomainTOTP, "v2-totp-secret")
	user, err := client.User.Create().
		SetEmail("v2-domain-migration@example.test").
		SetPasswordHash("password-hash").
		SetTotpSecretEncrypted(v2Ciphertext).
		SetTotpEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result, err := MigrateDomainBoundSecrets(ctx, client, encryptor)
	if err != nil {
		t.Fatal(err)
	}
	if result.TOTPSecrets != 1 {
		t.Fatalf("migration result = %#v", result)
	}
	refreshed, err := client.User.Get(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(*refreshed.TotpSecretEncrypted, secretDomainCiphertextPrefix) {
		t.Fatalf("migrated ciphertext = %q", *refreshed.TotpSecretEncrypted)
	}
	got, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainTOTP, *refreshed.TotpSecretEncrypted)
	if err != nil || got != "v2-totp-secret" {
		t.Fatalf("migrated v2 secret = %q, err=%v", got, err)
	}
}

func newDomainMigrationTestEncryptor(t *testing.T) service.SecretEncryptor {
	t.Helper()
	cfg := domainKeyTestConfig()
	cfg.Totp.EncryptionKey = strings.Repeat("f", 64)
	encryptor, err := NewAESEncryptor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return encryptor
}

func mustV2DomainEncrypt(t *testing.T, encryptor *AESEncryptor, domain, plaintext string) string {
	t.Helper()
	encoded, err := encryptAESGCM(
		deriveLegacyDomainKeyV2(encryptor.legacyKey, domain),
		legacySecretDomainAADV2(domain),
		plaintext,
	)
	if err != nil {
		t.Fatal(err)
	}
	return legacySecretDomainCiphertextPrefixV2 + encoded
}

func mustLegacyEncrypt(t *testing.T, encryptor service.SecretEncryptor, plaintext string) string {
	t.Helper()
	ciphertext, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return ciphertext
}
