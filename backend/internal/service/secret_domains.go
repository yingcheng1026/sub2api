package service

import "fmt"

const (
	SecretDomainTOTP              = "totp-secret"
	SecretDomainTOTPCache         = "totp-cache"
	SecretDomainAccountCredential = "account-credential"
	SecretDomainBackupS3          = "backup-s3"
	SecretDomainContentModeration = "content-moderation"
	SecretDomainChannelMonitor    = "channel-monitor"
	SecretDomainPaymentProvider   = "payment-provider"
	SecretDomainProxyCredential   = "proxy-credential"
	SecretDomainSchedulerCache    = "scheduler-cache"
	SecretDomainOAuthTokenCache   = "oauth-token-cache"
	SecretDomainJWTHMAC           = "jwt-hmac"
	SecretDomainSettingSecret     = "setting-secret"
)

// EncryptForSecretDomain refuses generic encryptors so runtime writes cannot
// silently fall back to relocatable legacy ciphertext.
func EncryptForSecretDomain(encryptor SecretEncryptor, domain, plaintext string) (string, error) {
	domainEncryptor, ok := encryptor.(DomainSecretEncryptor)
	if !ok || domainEncryptor == nil {
		return "", fmt.Errorf("domain secret encryptor is required for %s", domain)
	}
	return domainEncryptor.EncryptForDomain(domain, plaintext)
}

// DecryptForSecretDomain refuses legacy ciphertext. The startup security
// migration is the only code allowed to use generic Decrypt.
func DecryptForSecretDomain(encryptor SecretEncryptor, domain, ciphertext string) (string, error) {
	domainEncryptor, ok := encryptor.(DomainSecretEncryptor)
	if !ok || domainEncryptor == nil {
		return "", fmt.Errorf("domain secret encryptor is required for %s", domain)
	}
	return domainEncryptor.DecryptForDomain(domain, ciphertext)
}
