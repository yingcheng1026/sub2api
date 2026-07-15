package service

import "context"

// Fingerprint is retained for the provider-specific account-management cache.
// Shared Anthropic gateway requests never consume it as proof of official
// client identity.
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	UpdatedAt               int64 `json:",omitempty"`
}

// IdentityCache stores provider account-management fingerprints. It is not an
// authentication source for gateway client provenance.
type IdentityCache interface {
	GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error)
	SetFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) error
}
