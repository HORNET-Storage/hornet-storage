package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestSyncAirlockServiceDHTPubkeyFallsBackToRelayIdentity(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	airlockConfig := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(airlockConfig, []byte("private_key: \"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_CONFIG_PATH", airlockConfig)

	relayPrivateKey := "c600149fe1207dd0cf5284d0a4bd767dc192181940d2a2b08f9571445f308a02"
	viper.Set("relay.private_key", relayPrivateKey)
	expected, err := deriveAirlockDHTPublicKeyFromPrivateKey(relayPrivateKey, true)
	if err != nil {
		t.Fatal(err)
	}

	got, err := syncAirlockServiceDHTPubkey()
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected DHT public key %q, got %q", expected, got)
	}
}

func TestSyncAirlockServiceDHTPubkeyPrefersExplicitAirlockIdentity(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	airlockPrivateKey := "c600149fe1207dd0cf5284d0a4bd767dc192181940d2a2b08f9571445f308a02"
	airlockConfig := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(airlockConfig, []byte("private_key: "+airlockPrivateKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_CONFIG_PATH", airlockConfig)
	viper.Set("relay.private_key", "different-value")

	expected, err := deriveAirlockDHTPublicKeyFromPrivateKey(airlockPrivateKey, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := syncAirlockServiceDHTPubkey()
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected explicit Airlock DHT public key %q, got %q", expected, got)
	}
}

func TestRelayIdentityFallbackUsesDomainSeparatedAirlockDHTIdentity(t *testing.T) {
	privateKey := "c600149fe1207dd0cf5284d0a4bd767dc192181940d2a2b08f9571445f308a02"
	explicit, err := deriveAirlockDHTPublicKeyFromPrivateKey(privateKey, false)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := deriveAirlockDHTPublicKeyFromPrivateKey(privateKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if fallback == explicit {
		t.Fatal("expected relay identity fallback to use a domain-separated Airlock DHT identity")
	}

	const expectedDomainSeparatedSeed = "668e07e8a1a78580e0af6582533019c5898af2478004992954ca50e65bbb692b"
	seed, err := hex.DecodeString(expectedDomainSeparatedSeed)
	if err != nil {
		t.Fatal(err)
	}
	expectedPublicKey := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if fallback != expectedPublicKey {
		t.Fatalf("expected domain-separated Airlock DHT public key %q, got %q", expectedPublicKey, fallback)
	}
}
