package service

const (
	WalletDefaultOpenAIGroupName = "openai-default"
	WalletDefaultVIPGroupName    = "vip"
)

// CanUseWalletGroup applies the HFC credits-wallet authorization boundary.
// Credits are public only through openai-default. Other standard groups must
// be exclusive and explicitly granted to the user by an operator.
func CanUseWalletGroup(user *User, group *Group) bool {
	if user == nil || group == nil {
		return false
	}
	if err := validateReservedBusinessGroupState(group, true); err != nil {
		return false
	}
	switch group.Name {
	case WalletDefaultOpenAIGroupName:
		return true
	case WalletDefaultVIPGroupName:
		return user.CanBindGroup(group.ID, true)
	default:
		return false
	}
}
