package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

const oauthTokenCacheGenerationDomain = "sub2api/oauth-token-cache-generation/v1\x00"

// OpenAITokenCacheKey 生成 OpenAI OAuth 账号的缓存键
// 格式: "openai:account:{account_id}:cred:{generation}"
func OpenAITokenCacheKey(account *Account) string {
	return credentialBoundOAuthTokenCacheKey(openAITokenCacheBaseKey(account), account)
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}:cred:{generation}"
func ClaudeTokenCacheKey(account *Account) string {
	return credentialBoundOAuthTokenCacheKey(claudeTokenCacheBaseKey(account), account)
}

func openAITokenCacheBaseKey(account *Account) string {
	return "openai:account:" + strconv.FormatInt(account.ID, 10)
}

func claudeTokenCacheBaseKey(account *Account) string {
	return "claude:account:" + strconv.FormatInt(account.ID, 10)
}

func credentialBoundOAuthTokenCacheKey(base string, account *Account) string {
	generation := oauthTokenCacheGeneration(account)
	if generation == "" {
		return base
	}
	return base + ":cred:" + generation
}

// oauthTokenCacheGeneration binds a shared access-token cache entry to the
// credential generation that created it. Only a one-way digest is placed in
// the Redis key; access and refresh tokens are never embedded in key names.
func oauthTokenCacheGeneration(account *Account) string {
	if account == nil {
		return ""
	}
	accessToken := account.GetCredential("access_token")
	refreshToken := account.GetCredential("refresh_token")
	version := account.GetCredentialAsInt64("_token_version")
	if accessToken == "" && refreshToken == "" && version == 0 {
		return ""
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte(oauthTokenCacheGenerationDomain))
	writeOAuthTokenCacheGenerationField(hash, accessToken)
	writeOAuthTokenCacheGenerationField(hash, refreshToken)
	writeOAuthTokenCacheGenerationField(hash, strconv.FormatInt(version, 10))
	return hex.EncodeToString(hash.Sum(nil))
}

type oauthTokenCacheGenerationWriter interface {
	Write([]byte) (int, error)
}

func writeOAuthTokenCacheGenerationField(writer oauthTokenCacheGenerationWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
