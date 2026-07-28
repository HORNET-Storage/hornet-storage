package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSyncBootstrapAccessSettingsInviteOnlyForcesInviteOnlyReadAndWrite(t *testing.T) {
	relayConfig := map[string]interface{}{
		"allowed_users": map[string]interface{}{
			"mode":  "invite-only",
			"read":  "all_users",
			"write": "all_users",
		},
	}

	if err := syncBootstrapAccessSettings(relayConfig); err != nil {
		t.Fatalf("syncBootstrapAccessSettings returned error: %v", err)
	}

	allowedUsers, ok := relayConfig["allowed_users"].(map[string]interface{})
	if !ok {
		t.Fatal("expected allowed_users map to exist")
	}

	if got := allowedUsers["mode"]; got != "invite-only" {
		t.Fatalf("expected mode invite-only, got %#v", got)
	}
	if got := allowedUsers["read"]; got != "allowed_users" {
		t.Fatalf("expected read allowed_users, got %#v", got)
	}
	if got := allowedUsers["write"]; got != "allowed_users" {
		t.Fatalf("expected write allowed_users, got %#v", got)
	}
}

func TestSyncBootstrapAccessSettingsIgnoresOnlyMeAndForcesInviteOnly(t *testing.T) {
	relayConfig := map[string]interface{}{
		"allowed_users": map[string]interface{}{
			"mode":  "only-me",
			"read":  "all_users",
			"write": "allowed_users",
		},
	}

	if err := syncBootstrapAccessSettings(relayConfig); err != nil {
		t.Fatalf("syncBootstrapAccessSettings returned error: %v", err)
	}

	allowedUsers := relayConfig["allowed_users"].(map[string]interface{})
	if got := allowedUsers["mode"]; got != "invite-only" {
		t.Fatalf("expected mode invite-only, got %#v", got)
	}
	if got := allowedUsers["read"]; got != "allowed_users" {
		t.Fatalf("expected read allowed_users, got %#v", got)
	}
	if got := allowedUsers["write"]; got != "allowed_users" {
		t.Fatalf("expected write allowed_users, got %#v", got)
	}
}

func TestOperatorSetupBlankIdentityGeneratesStableProtectedConfiguration(t *testing.T) {
	session, err := newBootstrapSetupSession(BootstrapSetupProfileOperator, map[string]interface{}{
		"external_services": map[string]interface{}{
			"wallet": map[string]interface{}{"key": "existing-wallet-key"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	prepare := func() *setupPayload {
		payload := &setupPayload{}
		if err := prepareBootstrapSetupPayloadForSession(payload, session); err != nil {
			t.Fatalf("prepare operator payload: %v", err)
		}
		return payload
	}

	first := prepare()
	second := prepare()
	firstRelay := first.RelayConfig["relay"].(map[string]interface{})
	secondRelay := second.RelayConfig["relay"].(map[string]interface{})

	privateKey := stringSetting(firstRelay["private_key"])
	if privateKey == "" {
		t.Fatal("expected generated relay private key")
	}
	if got := stringSetting(secondRelay["private_key"]); got != privateKey {
		t.Fatal("validate/apply preparation must reuse the same generated relay identity")
	}
	derivedPublicKey, err := deriveRelayPublicKeyFromPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("generated private key is invalid: %v", err)
	}
	if got := stringSetting(firstRelay["public_key"]); got != derivedPublicKey {
		t.Fatalf("expected derived public key %q, got %q", derivedPublicKey, got)
	}
	if first.RelayOwnerPubkey != derivedPublicKey {
		t.Fatalf("expected generated relay identity to be the default owner, got %q", first.RelayOwnerPubkey)
	}

	secret := stringSetting(firstRelay["secret_key"])
	if len(secret) != 64 || secret == privateKey {
		t.Fatal("expected an independently generated 32-byte relay shared secret")
	}
	if got := stringSetting(secondRelay["secret_key"]); got != secret {
		t.Fatal("validate/apply preparation must reuse the same generated shared secret")
	}
	for _, key := range []string{"dht_seed", "dht_public_key", "dht_private_key"} {
		if stringSetting(firstRelay[key]) == "" {
			t.Fatalf("expected derived %s", key)
		}
	}

	externalServices := first.RelayConfig["external_services"].(map[string]interface{})
	wallet := externalServices["wallet"].(map[string]interface{})
	if wallet["key"] != "existing-wallet-key" {
		t.Fatalf("expected the server-held wallet key to be restored, got %#v", wallet["key"])
	}

	allowed := first.RelayConfig["allowed_users"].(map[string]interface{})
	if allowed["mode"] != "public" || allowed["read"] != "all_users" || allowed["write"] != "all_users" {
		t.Fatalf("unexpected operator access defaults: %#v", allowed)
	}
	server := first.RelayConfig["server"].(map[string]interface{})
	if server["upnp"] != false || server["bind_address"] != "0.0.0.0" || server["port"] != 11000 {
		t.Fatalf("unexpected operator server defaults: %#v", server)
	}
	if got := first.AirlockConfig["bind_address"]; got != "127.0.0.1" {
		t.Fatalf("expected loopback Airlock default, got %#v", got)
	}
}

func TestOperatorSetupPreservesAndValidatesExplicitSettings(t *testing.T) {
	privateKey, err := generateRelayPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	session, err := newBootstrapSetupSession(BootstrapSetupProfileOperator, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	payload := &setupPayload{
		RelayConfig: map[string]interface{}{
			"relay": map[string]interface{}{
				"private_key": privateKey,
				"secret_key":  "operator-selected-secret",
			},
			"server": map[string]interface{}{
				"bind_address": "127.0.0.1",
				"port":         12000,
				"upnp":         true,
				"data_path":    "custom-data",
			},
			"allowed_users": map[string]interface{}{"mode": "only-me"},
			"sidecar": map[string]interface{}{
				"address":    "127.0.0.1:9200",
				"mode":       "ephemeral",
				"executable": "custom-sidecar",
			},
		},
		AirlockConfig: map[string]interface{}{
			"bind_address":    "127.0.0.1",
			"port":            12006,
			"relay":           "127.0.0.1:12000",
			"repository_path": "custom-repositories",
			"sidecar": map[string]interface{}{
				"address": "127.0.0.1:9200",
				"mode":    "ephemeral",
			},
		},
		AirlockConfigPath: filepath.Join(t.TempDir(), "browser-selected.yaml"),
	}

	if err := prepareBootstrapSetupPayloadForSession(payload, session); err != nil {
		t.Fatalf("prepare explicit operator payload: %v", err)
	}
	relay := payload.RelayConfig["relay"].(map[string]interface{})
	server := payload.RelayConfig["server"].(map[string]interface{})
	allowed := payload.RelayConfig["allowed_users"].(map[string]interface{})
	if relay["private_key"] != privateKey || relay["secret_key"] != "operator-selected-secret" {
		t.Fatalf("explicit relay identity settings were not preserved: %#v", relay)
	}
	if server["bind_address"] != "127.0.0.1" || server["port"] != 12000 || server["upnp"] != true || server["data_path"] != "custom-data" {
		t.Fatalf("explicit server settings were not preserved: %#v", server)
	}
	if allowed["mode"] != "only-me" || allowed["read"] != "only-me" || allowed["write"] != "only-me" {
		t.Fatalf("operator access mode was not canonicalized: %#v", allowed)
	}
	if payload.AirlockConfig["port"] != 12006 || payload.AirlockConfig["repository_path"] != "custom-repositories" {
		t.Fatalf("explicit Airlock settings were not preserved: %#v", payload.AirlockConfig)
	}
	if payload.AirlockConfigPath != session.airlockConfigPath {
		t.Fatalf("browser-selected Airlock config target was trusted: got %q want %q", payload.AirlockConfigPath, session.airlockConfigPath)
	}
}

func TestOperatorSetupBrowserPayloadCannotChangeProfile(t *testing.T) {
	var payload setupPayload
	encoded, err := json.Marshal(map[string]interface{}{
		"profile":       "nosis",
		"relayConfig":   map[string]interface{}{},
		"airlockConfig": map[string]interface{}{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	session, err := newBootstrapSetupSession(BootstrapSetupProfileOperator, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareBootstrapSetupPayloadForSession(&payload, session); err != nil {
		t.Fatal(err)
	}
	allowed := payload.RelayConfig["allowed_users"].(map[string]interface{})
	if allowed["mode"] != "public" {
		t.Fatalf("browser payload changed server-owned setup profile: %#v", allowed)
	}
}

func TestOperatorDefaultsArePublicAndDoNotExposeSecrets(t *testing.T) {
	relayConfig := map[string]interface{}{
		"relay": map[string]interface{}{
			"private_key": "relay-private",
			"secret_key":  "relay-secret",
			"public_key":  "relay-public",
		},
		"server":        map[string]interface{}{"upnp": true},
		"allowed_users": map[string]interface{}{"mode": "invite-only"},
		"external_services": map[string]interface{}{
			"wallet": map[string]interface{}{"key": "wallet-secret"},
		},
	}
	airlockConfig := map[string]interface{}{
		"private_key":  "airlock-private",
		"bind_address": "0.0.0.0",
	}

	prepareOperatorSetupDefaults(relayConfig, airlockConfig)

	relay := relayConfig["relay"].(map[string]interface{})
	for _, key := range []string{"private_key", "secret_key", "public_key"} {
		if _, exists := relay[key]; exists {
			t.Fatalf("operator defaults exposed relay %s", key)
		}
	}
	if _, exists := airlockConfig["private_key"]; exists {
		t.Fatal("operator defaults exposed Airlock private_key")
	}
	if airlockConfig["bind_address"] != "127.0.0.1" {
		t.Fatalf("expected safe Airlock loopback default, got %#v", airlockConfig["bind_address"])
	}
	externalServices := relayConfig["external_services"].(map[string]interface{})
	wallet := externalServices["wallet"].(map[string]interface{})
	if _, exists := wallet["key"]; exists {
		t.Fatal("operator defaults exposed external_services.wallet.key")
	}
	allowed := relayConfig["allowed_users"].(map[string]interface{})
	server := relayConfig["server"].(map[string]interface{})
	if allowed["mode"] != "public" || server["upnp"] != false {
		t.Fatalf("unexpected safe operator defaults: access=%#v server=%#v", allowed, server)
	}
}

func TestNosisProfileStillRequiresIdentityAndForcesInviteOnly(t *testing.T) {
	payload := &setupPayload{
		RelayConfig: map[string]interface{}{
			"allowed_users": map[string]interface{}{"mode": "public", "read": "all_users", "write": "all_users"},
		},
	}
	err := prepareBootstrapSetupPayload(payload)
	if err == nil || !strings.Contains(err.Error(), "relay.private_key is required") {
		t.Fatalf("expected missing Nosis identity error, got %v", err)
	}
	allowed := payload.RelayConfig["allowed_users"].(map[string]interface{})
	if allowed["mode"] != "invite-only" || allowed["read"] != "allowed_users" || allowed["write"] != "allowed_users" {
		t.Fatalf("Nosis setup policy changed: %#v", allowed)
	}
}

func TestOperatorSetupRejectsInvalidConfiguredIdentityAndNetworkSettings(t *testing.T) {
	if _, err := newBootstrapSetupSession(BootstrapSetupProfileOperator, map[string]interface{}{
		"relay": map[string]interface{}{"private_key": "not-a-private-key"},
	}); err == nil {
		t.Fatal("expected an invalid preconfigured relay identity to be rejected")
	}

	session, err := newBootstrapSetupSession(BootstrapSetupProfileOperator, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		relayConfig map[string]interface{}
		airlock     map[string]interface{}
		wantError   string
	}{
		{
			name:        "port outside TCP range",
			relayConfig: map[string]interface{}{"server": map[string]interface{}{"port": 70000}},
			wantError:   "server.port",
		},
		{
			name:        "relay base port leaves no room for service offsets",
			relayConfig: map[string]interface{}{"server": map[string]interface{}{"port": 65531}},
			wantError:   "at most 65530",
		},
		{
			name:        "airlock port leaves no room for websocket proxy",
			relayConfig: map[string]interface{}{},
			airlock:     map[string]interface{}{"port": 65535},
			wantError:   "at most 65534",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := &setupPayload{RelayConfig: test.relayConfig, AirlockConfig: test.airlock}
			if err := prepareBootstrapSetupPayloadForSession(payload, session); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected network setting error containing %q, got %v", test.wantError, err)
			}
		})
	}
}

func TestParseBootstrapSetupProfileDefaultsToNosisAndRejectsUnknown(t *testing.T) {
	profile, err := parseBootstrapSetupProfile("")
	if err != nil || profile != BootstrapSetupProfileNosis {
		t.Fatalf("empty profile should select Nosis, profile=%q err=%v", profile, err)
	}
	if _, err := parseBootstrapSetupProfile("browser-selected"); err == nil {
		t.Fatal("expected unknown profile to be rejected")
	}
}

func TestWriteYAMLAtomicUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file permission bits")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeYAMLAtomic(path, map[string]interface{}{
		"relay": map[string]interface{}{"private_key": "secret"},
	}); err != nil {
		t.Fatalf("writeYAMLAtomic returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected config permissions 0600, got %04o", got)
	}
}
