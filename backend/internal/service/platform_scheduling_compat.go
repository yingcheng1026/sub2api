package service

func useMixedSchedulingForPlatform(platform string, hasForcePlatform bool) bool {
	if hasForcePlatform {
		return false
	}
	switch normalizePlatform(platform) {
	case PlatformAnthropic, PlatformGemini:
		return true
	default:
		return false
	}
}

func mixedSchedulingPlatforms(platform string) []string {
	switch normalizePlatform(platform) {
	case PlatformAnthropic:
		return []string{PlatformAnthropic, PlatformAntigravity, PlatformKiro}
	case PlatformGemini:
		return []string{PlatformGemini, PlatformAntigravity}
	default:
		return []string{platform}
	}
}

func isAccountAllowedInMixedScheduling(account *Account, platform string) bool {
	if account == nil {
		return false
	}
	accountPlatform := normalizePlatform(account.Platform)
	platform = normalizePlatform(platform)
	if accountPlatform == platform {
		return true
	}
	if platform == PlatformAnthropic && accountPlatform == PlatformKiro {
		return true
	}
	if accountPlatform == PlatformAntigravity {
		return account.IsMixedSchedulingEnabled() && (platform == PlatformAnthropic || platform == PlatformGemini)
	}
	return false
}
