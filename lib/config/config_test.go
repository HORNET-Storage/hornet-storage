package config

import (
	"os"
	"slices"
	"testing"

	"github.com/HORNET-Storage/hornet-storage/lib/types"
	"github.com/spf13/viper"
)

func TestConfigValueForLogRedactsSecrets(t *testing.T) {
	tests := []struct {
		key      string
		value    string
		expected interface{}
	}{
		{key: "relay.private_key", value: "private", expected: redactedConfigValue},
		{key: "relay.dht_seed", value: "seed", expected: redactedConfigValue},
		{key: "external_services.wallet.wallet_api_key", value: "api-key", expected: redactedConfigValue},
		{key: "auth-token", value: "token", expected: redactedConfigValue},
		{key: "relay.relay_public_key", value: "public", expected: "public"},
		{key: "relay.relay_name", value: "name", expected: "name"},
	}

	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			if got := configValueForLog(test.key, test.value); got != test.expected {
				t.Fatalf("expected %q to log as %v, got %v", test.key, test.expected, got)
			}
		})
	}
}

func TestNormalizeAllowedUsersConfigValuesFromLegacyNestedScope(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("allowed_users.mode", "public")
	viper.Set("allowed_users.read_access.scope", "all_users")
	viper.Set("allowed_users.write_access.scope", "paid_users")

	normalizeAllowedUsersConfigValues()

	if got := viper.GetString("allowed_users.read"); got != "all_users" {
		t.Fatalf("expected allowed_users.read to be normalized to all_users, got %q", got)
	}

	if got := viper.GetString("allowed_users.write"); got != "paid_users" {
		t.Fatalf("expected allowed_users.write to be normalized to paid_users, got %q", got)
	}
}

func TestNormalizeAllowedUsersConfigValuesKeepsFlatValues(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("allowed_users.read", "allowed_users")
	viper.Set("allowed_users.write", "only_me")
	viper.Set("allowed_users.mode", "invite_only")
	viper.Set("allowed_users.read_access.scope", "all_users")
	viper.Set("allowed_users.write_access.scope", "paid_users")

	normalizeAllowedUsersConfigValues()

	if got := viper.GetString("allowed_users.read"); got != "allowed_users" {
		t.Fatalf("expected flat allowed_users.read to be preserved, got %q", got)
	}

	if got := viper.GetString("allowed_users.write"); got != "only-me" {
		t.Fatalf("expected flat allowed_users.write to be normalized to only-me, got %q", got)
	}

	if got := viper.GetString("allowed_users.mode"); got != "invite-only" {
		t.Fatalf("expected flat allowed_users.mode to be normalized to invite-only, got %q", got)
	}
}

func TestNormalizeRelayDHTConfigValuesMigratesLegacyKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("relay.dht_key", "legacy-seed")

	normalizeRelayDHTConfigValues()

	if got := viper.GetString("relay.dht_seed"); got != "legacy-seed" {
		t.Fatalf("expected relay.dht_seed to be normalized from relay.dht_key, got %q", got)
	}
}

func TestNormalizeRelayDHTConfigValuesKeepsNewSeed(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("relay.dht_seed", "new-seed")
	viper.Set("relay.dht_key", "legacy-seed")

	normalizeRelayDHTConfigValues()

	if got := viper.GetString("relay.dht_seed"); got != "new-seed" {
		t.Fatalf("expected relay.dht_seed to be preserved, got %q", got)
	}
}

func TestNormalizeOrganizationKindsAppendsMissingValuesAndIsIdempotent(t *testing.T) {
	config := &types.Config{}
	config.EventFiltering.RegisteredKinds = []int{1, 39505}
	config.EventFiltering.KindWhitelist = []string{"kind1", "custom-handler", "kind39506"}

	normalizeOrganizationKinds(config)
	normalizeOrganizationKinds(config)

	expectedKinds := []int{1, 39505, 39504, 39506}
	if got := config.EventFiltering.RegisteredKinds; !slices.Equal(got, expectedKinds) {
		t.Fatalf("expected registered kinds %v, got %v", expectedKinds, got)
	}

	expectedWhitelist := []string{"kind1", "custom-handler", "kind39506", "kind39504", "kind39505"}
	if got := config.EventFiltering.KindWhitelist; !slices.Equal(got, expectedWhitelist) {
		t.Fatalf("expected kind whitelist %v, got %v", expectedWhitelist, got)
	}
}

func TestSetDefaultsUsesRestrictedAccessForFreshInstall(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	setDefaults()

	if got := viper.GetString("allowed_users.mode"); got != "invite-only" {
		t.Fatalf("expected fresh-install mode invite-only, got %q", got)
	}
	if got := viper.GetString("allowed_users.read"); got != "allowed_users" {
		t.Fatalf("expected fresh-install read scope allowed_users, got %q", got)
	}
	if got := viper.GetString("allowed_users.write"); got != "allowed_users" {
		t.Fatalf("expected fresh-install write scope allowed_users, got %q", got)
	}
}
