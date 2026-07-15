package service

import "testing"

func TestCanUseWalletGroupRequiresExactHFCGroupIdentity(t *testing.T) {
	fallbackID := int64(99)
	tests := []struct {
		name  string
		user  *User
		group *Group
		want  bool
	}{
		{
			name: "openai default",
			user: &User{ID: 1},
			group: &Group{ID: 3, Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, SubscriptionType: SubscriptionTypeStandard},
			want: true,
		},
		{
			name: "openai name on wrong platform",
			user: &User{ID: 1},
			group: &Group{ID: 30, Name: WalletDefaultOpenAIGroupName, Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "exclusive openai default fails closed",
			user: &User{ID: 1},
			group: &Group{ID: 31, Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "openai default with fallback fails closed",
			user: &User{ID: 1},
			group: &Group{ID: 32, Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, SubscriptionType: SubscriptionTypeStandard, FallbackGroupID: &fallbackID},
			want: false,
		},
		{
			name: "exact vip with explicit grant",
			user: &User{ID: 1, AllowedGroups: []int64{22}},
			group: &Group{ID: 22, Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: true,
		},
		{
			name: "vip without grant",
			user: &User{ID: 1},
			group: &Group{ID: 22, Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "vip with invalid request fallback fails closed",
			user: &User{ID: 1, AllowedGroups: []int64{224}},
			group: &Group{ID: 224, Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard,
				FallbackGroupIDOnInvalidRequest: &fallbackID},
			want: false,
		},
		{
			name: "case variant is not the dedicated vip group",
			user: &User{ID: 1, AllowedGroups: []int64{220}},
			group: &Group{ID: 220, Name: "VIP", Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "whitespace variant is not openai default",
			user: &User{ID: 1},
			group: &Group{ID: 222, Name: " openai-default ", Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "whitespace variant is not vip",
			user: &User{ID: 1, AllowedGroups: []int64{223}},
			group: &Group{ID: 223, Name: " vip ", Platform: PlatformAnthropic, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
		{
			name: "vip on wrong platform",
			user: &User{ID: 1, AllowedGroups: []int64{221}},
			group: &Group{ID: 221, Name: WalletDefaultVIPGroupName, Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanUseWalletGroup(tt.user, tt.group); got != tt.want {
				t.Fatalf("CanUseWalletGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}
