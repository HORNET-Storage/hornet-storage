package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	statistics_gorm_sqlite "github.com/HORNET-Storage/hornet-storage/lib/stores/statistics/gorm/sqlite"
	synckeys "github.com/HORNET-Storage/hornet-storage/lib/sync"
	"github.com/gofiber/fiber/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type setupPayload struct {
	RelayConfig       map[string]interface{} `json:"relayConfig"`
	AirlockConfig     map[string]interface{} `json:"airlockConfig"`
	AirlockConfigPath string                 `json:"airlockConfigPath"`
	RelayOwnerPubkey  string                 `json:"relayOwnerPubkey"`
}

// BootstrapSetupProfile selects the server-owned first-run policy. It is never
// accepted from the browser payload, so a client cannot swap setup policy.
type BootstrapSetupProfile string

const (
	BootstrapSetupProfileNosis    BootstrapSetupProfile = "nosis"
	BootstrapSetupProfileOperator BootstrapSetupProfile = "operator"
)

type bootstrapSetupSession struct {
	profile           BootstrapSetupProfile
	relayPrivateKey   string
	relaySecret       string
	walletAPIKey      string
	airlockConfigPath string
}

func setupMarkerPath() string {
	dataPath := viper.GetString("server.data_path")
	if dataPath == "" {
		dataPath = "./data"
	}
	return filepath.Join(dataPath, ".hornets_setup_complete")
}

func randomHex(byteCount int) (string, error) {
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func setupToken() (string, error) {
	return randomHex(24)
}

func parseBootstrapSetupProfile(value string) (BootstrapSetupProfile, error) {
	switch BootstrapSetupProfile(strings.ToLower(strings.TrimSpace(value))) {
	case "", BootstrapSetupProfileNosis:
		return BootstrapSetupProfileNosis, nil
	case BootstrapSetupProfileOperator:
		return BootstrapSetupProfileOperator, nil
	default:
		return "", fmt.Errorf("unknown bootstrap setup profile %q", value)
	}
}

func generateRelayPrivateKey() (string, error) {
	privateKey, err := signing.GeneratePrivateKey()
	if err != nil {
		return "", fmt.Errorf("failed to generate relay private key: %w", err)
	}
	serialized, err := signing.SerializePrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to serialize relay private key: %w", err)
	}
	return *serialized, nil
}

func newBootstrapSetupSession(profile BootstrapSetupProfile, relayDefaults map[string]interface{}) (*bootstrapSetupSession, error) {
	session := &bootstrapSetupSession{profile: profile}
	if profile == BootstrapSetupProfileNosis {
		return session, nil
	}
	if profile != BootstrapSetupProfileOperator {
		return nil, fmt.Errorf("unsupported bootstrap setup profile %q", profile)
	}
	session.airlockConfigPath = defaultAirlockConfigPath()
	externalServices, _ := relayDefaults["external_services"].(map[string]interface{})
	walletCfg, _ := externalServices["wallet"].(map[string]interface{})
	session.walletAPIKey = stringSetting(walletCfg["key"])

	relayCfg, _ := relayDefaults["relay"].(map[string]interface{})
	existingPrivateKey := stringSetting(relayCfg["private_key"])
	if existingPrivateKey != "" {
		if _, err := deriveRelayPublicKeyFromPrivateKey(existingPrivateKey); err != nil {
			return nil, fmt.Errorf("configured relay private key is invalid: %w", err)
		}
		session.relayPrivateKey = existingPrivateKey
	}
	if session.relayPrivateKey == "" {
		generatedPrivateKey, err := generateRelayPrivateKey()
		if err != nil {
			return nil, err
		}
		session.relayPrivateKey = generatedPrivateKey
	}

	session.relaySecret = stringSetting(relayCfg["secret_key"])
	if session.relaySecret == "" {
		generatedSecret, err := randomHex(32)
		if err != nil {
			return nil, fmt.Errorf("failed to generate relay shared secret: %w", err)
		}
		session.relaySecret = generatedSecret
	}

	return session, nil
}

func writeYAMLAtomic(path string, data map[string]interface{}) error {
	if len(data) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, encoded, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeSetupMarker() error {
	return writeSetupMarkerForConfig(nil)
}

func writeSetupMarkerForConfig(relayConfig map[string]interface{}) error {
	markerPaths := map[string]struct{}{setupMarkerPath(): {}}
	if relayConfig != nil {
		markerPaths[filepath.Join(relayDataPath(relayConfig), ".hornets_setup_complete")] = struct{}{}
	}

	for marker := range markerPaths {
		if err := os.MkdirAll(filepath.Dir(marker), os.ModePerm); err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
			return err
		}
	}
	return nil
}

func needsBootstrapSetup() bool {
	_, err := os.Stat(setupMarkerPath())
	return errors.Is(err, os.ErrNotExist)
}

func defaultAirlockConfigPath() string {
	path := os.Getenv("AIRLOCK_CONFIG_PATH")
	if path == "" {
		path = filepath.Join("..", "airlock", "config.yaml")
	}
	return path
}

func readYAMLMap(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{}
	}
	parsed := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return map[string]interface{}{}
	}
	if parsed == nil {
		return map[string]interface{}{}
	}
	return parsed
}

func readPreferredYAMLMap(paths ...string) map[string]interface{} {
	for _, path := range paths {
		parsed := readYAMLMap(path)
		if len(parsed) > 0 {
			return parsed
		}
	}
	return map[string]interface{}{}
}

func ensureNestedMap(parent map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := parent[key].(map[string]interface{}); ok && existing != nil {
		return existing
	}

	nested := map[string]interface{}{}
	parent[key] = nested
	return nested
}

func stringSetting(value interface{}) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func integerSetting(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		if uint64(int(typed)) != typed {
			return 0, false
		}
		return int(typed), true
	case float32:
		parsed := int(typed)
		return parsed, float32(parsed) == typed
	case float64:
		parsed := int(typed)
		return parsed, float64(parsed) == typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		parsed, err := strconv.Atoi(stringSetting(value))
		return parsed, err == nil
	}
}

func normalizePort(value interface{}, fallback int, fieldName string) (int, error) {
	if stringSetting(value) == "" {
		return fallback, nil
	}
	port, ok := integerSetting(value)
	if !ok || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535", fieldName)
	}
	return port, nil
}

func normalizeBool(value interface{}, fallback bool, fieldName string) (bool, error) {
	if value == nil || stringSetting(value) == "" {
		return fallback, nil
	}
	if parsed, ok := value.(bool); ok {
		return parsed, nil
	}
	parsed, err := strconv.ParseBool(stringSetting(value))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", fieldName)
	}
	return parsed, nil
}

func normalizeIPv4BindAddress(value interface{}, fallback string, fieldName string) (string, error) {
	address := stringSetting(value)
	if address == "" {
		address = fallback
	}
	parsed := net.ParseIP(address)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("%s must be a valid IPv4 bind address", fieldName)
	}
	return address, nil
}

func validateHostPort(value string, fieldName string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s must be a host:port address", fieldName)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%s must use a port between 1 and 65535", fieldName)
	}
	return nil
}

func syncBootstrapAccessSettings(relayConfig map[string]interface{}) error {
	if relayConfig == nil {
		return nil
	}

	allowedUsers := ensureNestedMap(relayConfig, "allowed_users")
	allowedUsers["mode"] = "invite-only"
	allowedUsers["read"] = "allowed_users"
	allowedUsers["write"] = "allowed_users"
	allowedUsers["last_updated"] = time.Now().Unix()
	return nil
}

func syncOperatorAccessSettings(relayConfig map[string]interface{}) error {
	allowedUsers := ensureNestedMap(relayConfig, "allowed_users")
	mode := strings.ToLower(stringSetting(allowedUsers["mode"]))
	if mode == "" {
		mode = "public"
	}

	switch mode {
	case "public":
		allowedUsers["read"] = "all_users"
		allowedUsers["write"] = "all_users"
	case "invite-only":
		allowedUsers["read"] = "allowed_users"
		allowedUsers["write"] = "allowed_users"
	case "only-me":
		allowedUsers["read"] = "only-me"
		allowedUsers["write"] = "only-me"
	case "subscription":
		allowedUsers["read"] = "paid_users"
		allowedUsers["write"] = "paid_users"
	default:
		return fmt.Errorf("allowed_users.mode must be public, invite-only, only-me, or subscription")
	}

	allowedUsers["mode"] = mode
	allowedUsers["last_updated"] = time.Now().Unix()
	return nil
}

func prepareOperatorSetupDefaults(relayConfig map[string]interface{}, airlockConfig map[string]interface{}) {
	allowedUsers := ensureNestedMap(relayConfig, "allowed_users")
	allowedUsers["mode"] = "public"
	allowedUsers["read"] = "all_users"
	allowedUsers["write"] = "all_users"

	serverCfg := ensureNestedMap(relayConfig, "server")
	if stringSetting(serverCfg["bind_address"]) == "" {
		serverCfg["bind_address"] = "0.0.0.0"
	}
	if stringSetting(serverCfg["port"]) == "" {
		serverCfg["port"] = 11000
	}
	serverCfg["upnp"] = false
	if stringSetting(serverCfg["data_path"]) == "" {
		serverCfg["data_path"] = "./data"
	}

	relayCfg := ensureNestedMap(relayConfig, "relay")
	delete(relayCfg, "private_key")
	delete(relayCfg, "secret_key")
	delete(relayCfg, "public_key")
	delete(relayCfg, "dht_seed")
	delete(relayCfg, "dht_public_key")
	delete(relayCfg, "dht_private_key")
	delete(relayCfg, "dht_key")

	externalServices := ensureNestedMap(relayConfig, "external_services")
	walletCfg := ensureNestedMap(externalServices, "wallet")
	delete(walletCfg, "key")

	relaySidecar := ensureNestedMap(relayConfig, "sidecar")
	if stringSetting(relaySidecar["address"]) == "" {
		relaySidecar["address"] = "127.0.0.1:9100"
	}
	if stringSetting(relaySidecar["mode"]) == "" {
		relaySidecar["mode"] = "persistent"
	}

	delete(airlockConfig, "private_key")
	airlockConfig["bind_address"] = "127.0.0.1"
	if stringSetting(airlockConfig["port"]) == "" {
		airlockConfig["port"] = 11006
	}
	if stringSetting(airlockConfig["relay"]) == "" {
		airlockConfig["relay"] = "127.0.0.1:11000"
	}
	if stringSetting(airlockConfig["repository_path"]) == "" {
		airlockConfig["repository_path"] = "repositories"
	}

	airlockSidecar := ensureNestedMap(airlockConfig, "sidecar")
	if stringSetting(airlockSidecar["address"]) == "" {
		airlockSidecar["address"] = stringSetting(relaySidecar["address"])
	}
	if stringSetting(airlockSidecar["mode"]) == "" {
		airlockSidecar["mode"] = stringSetting(relaySidecar["mode"])
	}
}

func deriveRelayPublicKeyFromPrivateKey(privateKey string) (string, error) {
	_, publicKey, err := signing.DeserializePrivateKey(strings.TrimSpace(privateKey))
	if err != nil {
		return "", fmt.Errorf("invalid relay private key: %w", err)
	}

	serializedPublicKey, err := signing.SerializePublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to serialize relay public key: %w", err)
	}

	return *serializedPublicKey, nil
}

func normalizeRelayOwnerPubkey(value string, defaultPubkey string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultPubkey, nil
	}

	publicKey, err := signing.DeserializePublicKey(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid relay owner public key: %w", err)
	}

	serializedPublicKey, err := signing.SerializePublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to serialize relay owner public key: %w", err)
	}

	return *serializedPublicKey, nil
}

func relayDataPath(relayConfig map[string]interface{}) string {
	serverCfg, _ := relayConfig["server"].(map[string]interface{})
	dataPath := stringSetting(serverCfg["data_path"])
	if dataPath == "" {
		return "./data"
	}
	return dataPath
}

func persistBootstrapRelayOwner(relayConfig map[string]interface{}, relayOwnerPubkey string) error {
	if strings.TrimSpace(relayOwnerPubkey) == "" {
		return nil
	}

	statsDBPath := filepath.Join(relayDataPath(relayConfig), "statistics", "statistics.db")
	statsStore, err := statistics_gorm_sqlite.InitStore(statsDBPath)
	if err != nil {
		return err
	}
	defer statsStore.Close()

	return statsStore.SetRelayOwner(relayOwnerPubkey, "bootstrap_setup")
}

func prepareBootstrapSetupPayload(payload *setupPayload) error {
	return prepareBootstrapSetupPayloadForSession(payload, &bootstrapSetupSession{profile: BootstrapSetupProfileNosis})
}

func prepareBootstrapSetupPayloadForSession(payload *setupPayload, session *bootstrapSetupSession) error {
	if payload == nil {
		return fmt.Errorf("setup payload is required")
	}
	if session == nil {
		return fmt.Errorf("bootstrap setup session is required")
	}
	if payload.RelayConfig == nil {
		payload.RelayConfig = map[string]interface{}{}
	}
	if payload.AirlockConfig == nil {
		payload.AirlockConfig = map[string]interface{}{}
	}

	relayCfg := ensureNestedMap(payload.RelayConfig, "relay")
	privateKey := stringSetting(relayCfg["private_key"])

	switch session.profile {
	case BootstrapSetupProfileNosis:
		if err := syncBootstrapAccessSettings(payload.RelayConfig); err != nil {
			return err
		}
		if privateKey == "" {
			return fmt.Errorf("relay.private_key is required")
		}
	case BootstrapSetupProfileOperator:
		payload.AirlockConfigPath = session.airlockConfigPath
		externalServices := ensureNestedMap(payload.RelayConfig, "external_services")
		walletCfg := ensureNestedMap(externalServices, "wallet")
		if stringSetting(walletCfg["key"]) == "" && session.walletAPIKey != "" {
			walletCfg["key"] = session.walletAPIKey
		}
		if privateKey == "" {
			privateKey = session.relayPrivateKey
			relayCfg["private_key"] = privateKey
		}
		if privateKey == "" {
			return fmt.Errorf("operator relay identity is unavailable")
		}
		if stringSetting(relayCfg["secret_key"]) == "" {
			relayCfg["secret_key"] = session.relaySecret
		}
		if err := syncOperatorAccessSettings(payload.RelayConfig); err != nil {
			return err
		}
		if err := normalizeOperatorNetworkSettings(payload); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported bootstrap setup profile %q", session.profile)
	}

	derivedRelayPubkey, err := deriveRelayPublicKeyFromPrivateKey(privateKey)
	if err != nil {
		return err
	}
	configuredRelayPubkey := stringSetting(relayCfg["public_key"])
	if session.profile == BootstrapSetupProfileOperator && configuredRelayPubkey != "" {
		normalizedConfiguredPubkey, err := normalizeRelayOwnerPubkey(configuredRelayPubkey, "")
		if err != nil {
			return fmt.Errorf("invalid relay.public_key: %w", err)
		}
		if normalizedConfiguredPubkey != derivedRelayPubkey {
			return fmt.Errorf("relay.public_key does not match relay.private_key")
		}
	}
	if configuredRelayPubkey == "" || session.profile == BootstrapSetupProfileOperator {
		relayCfg["public_key"] = derivedRelayPubkey
	}

	if session.profile == BootstrapSetupProfileOperator {
		if err := syncOperatorDHTIdentity(relayCfg, privateKey); err != nil {
			return err
		}
	}

	relayOwnerPubkey, err := normalizeRelayOwnerPubkey(payload.RelayOwnerPubkey, derivedRelayPubkey)
	if err != nil {
		return err
	}
	payload.RelayOwnerPubkey = relayOwnerPubkey

	return syncAirlockDHTPubkeyIntoRelayConfig(payload.RelayConfig, payload.AirlockConfig)
}

func syncOperatorDHTIdentity(relayCfg map[string]interface{}, privateKey string) error {
	dhtSeed := stringSetting(relayCfg["dht_seed"])
	if dhtSeed == "" {
		dhtSeed = stringSetting(relayCfg["dht_key"])
	}

	var (
		identity *synckeys.DHTIdentity
		err      error
	)
	if dhtSeed == "" {
		identity, err = synckeys.DeriveDHTIdentityFromPrivateKey(privateKey)
	} else {
		identity, err = synckeys.DeriveDHTIdentityFromSeed(dhtSeed)
	}
	if err != nil {
		return fmt.Errorf("invalid relay DHT identity: %w", err)
	}

	relayCfg["dht_seed"] = identity.Seed
	relayCfg["dht_public_key"] = identity.PublicKey
	relayCfg["dht_private_key"] = identity.PrivateKey
	delete(relayCfg, "dht_key")
	return nil
}

func normalizeOperatorNetworkSettings(payload *setupPayload) error {
	serverCfg := ensureNestedMap(payload.RelayConfig, "server")
	bindAddress, err := normalizeIPv4BindAddress(serverCfg["bind_address"], "0.0.0.0", "server.bind_address")
	if err != nil {
		return err
	}
	serverPort, err := normalizePort(serverCfg["port"], 11000, "server.port")
	if err != nil {
		return err
	}
	if serverPort > 65530 {
		return fmt.Errorf("server.port must be at most 65530 because relay services use ports through server.port + 5")
	}
	upnpEnabled, err := normalizeBool(serverCfg["upnp"], false, "server.upnp")
	if err != nil {
		return err
	}
	dataPath := stringSetting(serverCfg["data_path"])
	if dataPath == "" {
		dataPath = "./data"
	}
	serverCfg["bind_address"] = bindAddress
	serverCfg["port"] = serverPort
	serverCfg["upnp"] = upnpEnabled
	serverCfg["data_path"] = dataPath

	relaySidecar := ensureNestedMap(payload.RelayConfig, "sidecar")
	relaySidecarAddress := stringSetting(relaySidecar["address"])
	if relaySidecarAddress == "" {
		relaySidecarAddress = "127.0.0.1:9100"
	}
	if err := validateHostPort(relaySidecarAddress, "sidecar.address"); err != nil {
		return err
	}
	relaySidecarMode := strings.ToLower(stringSetting(relaySidecar["mode"]))
	if relaySidecarMode == "" {
		relaySidecarMode = "persistent"
	}
	if relaySidecarMode != "persistent" && relaySidecarMode != "ephemeral" {
		return fmt.Errorf("sidecar.mode must be persistent or ephemeral")
	}
	relaySidecar["address"] = relaySidecarAddress
	relaySidecar["mode"] = relaySidecarMode

	airlockBindAddress, err := normalizeIPv4BindAddress(payload.AirlockConfig["bind_address"], "127.0.0.1", "airlock.bind_address")
	if err != nil {
		return err
	}
	airlockPort, err := normalizePort(payload.AirlockConfig["port"], 11006, "airlock.port")
	if err != nil {
		return err
	}
	if airlockPort > 65534 {
		return fmt.Errorf("airlock.port must be at most 65534 because its WebSocket proxy uses airlock.port + 1")
	}
	airlockRelay := stringSetting(payload.AirlockConfig["relay"])
	if airlockRelay == "" {
		airlockRelay = fmt.Sprintf("127.0.0.1:%d", serverPort)
	}
	if err := validateHostPort(airlockRelay, "airlock.relay"); err != nil {
		return err
	}
	repositoryPath := stringSetting(payload.AirlockConfig["repository_path"])
	if repositoryPath == "" {
		repositoryPath = "repositories"
	}
	payload.AirlockConfig["bind_address"] = airlockBindAddress
	payload.AirlockConfig["port"] = airlockPort
	payload.AirlockConfig["relay"] = airlockRelay
	payload.AirlockConfig["repository_path"] = repositoryPath

	airlockSidecar := ensureNestedMap(payload.AirlockConfig, "sidecar")
	airlockSidecarAddress := stringSetting(airlockSidecar["address"])
	if airlockSidecarAddress == "" {
		airlockSidecarAddress = relaySidecarAddress
	}
	if err := validateHostPort(airlockSidecarAddress, "airlock.sidecar.address"); err != nil {
		return err
	}
	airlockSidecarMode := strings.ToLower(stringSetting(airlockSidecar["mode"]))
	if airlockSidecarMode == "" {
		airlockSidecarMode = relaySidecarMode
	}
	if airlockSidecarMode != "persistent" && airlockSidecarMode != "ephemeral" {
		return fmt.Errorf("airlock.sidecar.mode must be persistent or ephemeral")
	}
	airlockSidecar["address"] = airlockSidecarAddress
	airlockSidecar["mode"] = airlockSidecarMode
	return nil
}

func syncAirlockDHTPubkeyIntoRelayConfig(relayConfig map[string]interface{}, airlockConfig map[string]interface{}) error {
	if relayConfig == nil {
		return nil
	}

	privateKey := stringSetting(airlockConfig["private_key"])
	domainSeparated := false
	if privateKey == "" {
		relayCfg, _ := relayConfig["relay"].(map[string]interface{})
		privateKey = stringSetting(relayCfg["private_key"])
		domainSeparated = true
	}
	if privateKey == "" {
		return nil
	}

	airlockDHTPublicKey, err := deriveAirlockDHTPublicKeyFromPrivateKey(privateKey, domainSeparated)
	if err != nil {
		return err
	}

	serverCfg := ensureNestedMap(relayConfig, "server")
	servicesCfg := ensureNestedMap(serverCfg, "services")
	airlockServiceCfg := ensureNestedMap(servicesCfg, "airlock")
	airlockServiceCfg["dht_pubkey"] = airlockDHTPublicKey
	return nil
}

func renderBootstrapSetupPage(token string) string {
	return fmt.Sprintf(`<!doctype html>
<html>
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<title>HORNETS First-Time Setup</title>
	<style>
		:root {
			--bg: #0d131c;
			--panel: #111a26;
			--panel-strong: #0f1722;
			--border: #26364d;
			--text: #edf3fb;
			--muted: #99adc4;
			--accent: #5fc97a;
			--accent-strong: #b8f0c5;
			--secondary: #1f2d42;
			--warning: #f3bf62;
			--danger: #ff8f8f;
		}
		* { box-sizing: border-box; }
		body {
			margin: 0;
			font-family: "Segoe UI", sans-serif;
			background: radial-gradient(circle at top left, rgba(95, 201, 122, 0.12), transparent 60%%), linear-gradient(180deg, #0a0f16, var(--bg));
			color: var(--text);
		}
		body.embedded {
			background: transparent;
		}
		.shell {
			max-width: 980px;
			margin: 0 auto;
			padding: 20px;
		}
		body.embedded .shell {
			max-width: none;
			padding: 12px;
		}
		.hero {
			display: grid;
			gap: 14px;
			margin-bottom: 16px;
		}
		@media (min-width: 920px) {
			.hero {
				grid-template-columns: minmax(0, 1.15fr) minmax(280px, 0.85fr);
			}
		}
		.card {
			background: linear-gradient(180deg, rgba(255,255,255,0.02), rgba(255,255,255,0.01));
			border: 1px solid var(--border);
			border-radius: 18px;
			padding: 18px;
			box-shadow: 0 20px 60px rgba(0, 0, 0, 0.18);
		}
		.lead {
			display: grid;
			gap: 12px;
		}
		.eyebrow {
			display: inline-flex;
			align-items: center;
			gap: 8px;
			font-size: 12px;
			letter-spacing: 0.16em;
			text-transform: uppercase;
			color: var(--accent-strong);
		}
		.hero h1 {
			margin: 0;
			font-size: clamp(30px, 4vw, 42px);
			line-height: 1.05;
		}
		.hero p,
		.hint,
		.note,
		label,
		summary,
		.checklist {
			color: var(--muted);
		}
		.hint,
		.note,
		label,
		summary {
			font-size: 13px;
		}
		.checklist {
			margin: 0;
			padding-left: 18px;
			display: grid;
			gap: 10px;
		}
		.check-field {
			gap: 8px;
		}
		.check-row {
			display: inline-flex;
			align-items: center;
			gap: 10px;
			color: var(--text);
			font-weight: 700;
		}
		.check-row input {
			width: auto;
			margin: 0;
			accent-color: var(--accent);
		}
		h2,
		h3 {
			margin: 0 0 10px;
		}
		h2 {
			font-size: 20px;
		}
		h3 {
			font-size: 15px;
		}
		.form-grid,
		.advanced-grid {
			display: grid;
			gap: 12px;
		}
		@media (min-width: 760px) {
			.form-grid {
				grid-template-columns: 1fr 1fr;
			}
			.form-grid .full,
			.advanced-grid .full {
				grid-column: 1 / -1;
			}
			.advanced-grid {
				grid-template-columns: 1fr 1fr;
			}
		}
		.field {
			display: grid;
			gap: 6px;
		}
		input,
		textarea,
		select {
			width: 100%%;
			border: 1px solid var(--border);
			border-radius: 12px;
			background: var(--panel-strong);
			color: var(--text);
			padding: 11px 12px;
			font-size: 14px;
			outline: none;
		}
		textarea {
			min-height: 120px;
			resize: vertical;
			font-family: Consolas, monospace;
		}
		input:focus,
		textarea:focus,
		select:focus {
			border-color: var(--accent);
			box-shadow: 0 0 0 3px rgba(95, 201, 122, 0.14);
		}
		.key-input {
			font-family: Consolas, monospace;
		}
		.inline-note {
			padding: 12px 14px;
			border-radius: 14px;
			background: rgba(31, 45, 66, 0.7);
			border: 1px solid rgba(184, 240, 197, 0.18);
		}
		.actions {
			display: flex;
			gap: 10px;
			margin-top: 16px;
			flex-wrap: wrap;
		}
		button {
			border: 0;
			border-radius: 999px;
			padding: 11px 16px;
			font-size: 14px;
			font-weight: 700;
			cursor: pointer;
		}
		button.primary {
			background: var(--accent);
			color: #0c2213;
		}
		button.secondary {
			background: var(--secondary);
			color: var(--text);
		}
		button.ghost {
			background: transparent;
			color: var(--muted);
			border: 1px solid var(--border);
		}
		details {
			margin-top: 14px;
		}
		summary {
			cursor: pointer;
			font-weight: 700;
		}
		.subcard {
			background: rgba(15, 23, 34, 0.76);
			border: 1px solid rgba(255,255,255,0.05);
			border-radius: 14px;
			padding: 14px;
		}
		.status {
			margin-top: 14px;
			padding: 12px 14px;
			border-radius: 14px;
			border: 1px solid var(--border);
			background: rgba(15, 23, 34, 0.82);
			white-space: pre-wrap;
			font-size: 13px;
		}
		.status.ok {
			border-color: rgba(95, 201, 122, 0.55);
		}
		.status.bad {
			border-color: rgba(255, 143, 143, 0.55);
			color: #ffdada;
		}
		.preview textarea {
			min-height: 260px;
		}
		.tip {
			color: var(--warning);
			font-size: 12px;
		}
	</style>
</head>
<body>
	<div class="shell">
		<section class="hero">
			<div class="card lead">
				<div class="eyebrow">First-time setup</div>
				<h1>Bring your relay online.</h1>
				<p>Set your relay identity once and keep the rest automatic. Airlock reads this identity from the sibling relay config unless you explicitly override it in Advanced.</p>
				<div class="inline-note">
					<div class="note">Default flow</div>
					<div>Only your relay name, icon, optional description/contact, and private key belong on the first screen.</div>
				</div>
			</div>
			<div class="card">
				<h2>What happens next</h2>
				<ul class="checklist">
					<li>Relay public and DHT keys are derived from the relay private key when left blank.</li>
					<li>Airlock derives a domain-separated DHT identity from the relay key without storing a second copy.</li>
					<li>The colocated hyperswarm sidecar is discovered automatically.</li>
					<li>Advanced overrides stay available for custom paths, sidecar settings, and raw JSON patches.</li>
				</ul>
			</div>
		</section>

		<section class="card">
			<h2>Required Setup</h2>
			<div class="hint">Public key, DHT key, and most Airlock settings are handled automatically. Paste your relay private key and keep the rest simple unless you need a custom layout.</div>
			<div class="form-grid">
				<div class="field">
					<label for="relay_name">Relay name</label>
					<input id="relay_name" placeholder="Hornet Storage">
				</div>
				<div class="field">
					<label for="relay_icon">Icon URL</label>
					<input id="relay_icon" placeholder="https://example.com/logo.png">
				</div>
				<div class="field full">
					<label for="relay_description">Description</label>
					<input id="relay_description" placeholder="Describe this relay for clients discovering it.">
				</div>
				<div class="field">
					<label for="relay_contact">Contact email</label>
					<input id="relay_contact" placeholder="support@example.com">
				</div>
				<div class="field full">
					<label for="relay_private_key">Relay private key</label>
					<input id="relay_private_key" class="key-input" placeholder="hex or nsec key used to identify this relay">
					<div class="tip">Airlock will derive its own domain-separated DHT identity from this key unless you set a separate Airlock key in Advanced.</div>
					<div id="relay_private_key_lock_hint" class="tip" hidden>Nosis supplied the signed-in private key for this relay. It is locked here to prevent invite-only access mismatches.</div>
				</div>
				<div class="field full subcard">
					<h3>Relay access</h3>
					<div class="field">
						<label>Repository access</label>
						<div id="read_access_mode_hint" class="tip">When creating or importing a repo, you can choose if read access is public or invite-only. Write access is gated to contributors. This is configured on the repo setup page.</div>
					</div>
					<div class="field">
						<label for="relay_owner_pubkey">Relay owner public key</label>
						<input id="relay_owner_pubkey" class="key-input" placeholder="leave blank to use the relay private key's public key">
						<div id="relay_owner_pubkey_lock_hint" class="tip" hidden>The relay owner is locked to the signed-in Nosis account.</div>
					</div>
				</div>
			</div>
			<div class="actions">
				<button class="primary" type="button" onclick="applySetup()">Apply Setup</button>
			</div>
			<div id="status" class="status">Waiting for input...</div>
		</section>

		<details class="card">
			<summary>Advanced</summary>
			<div class="hint">Only open this if you want to override derived relay fields, Airlock defaults, or the generated payload.</div>
			<div class="advanced-grid">
				<section class="subcard">
					<h3>Relay Overrides</h3>
					<div class="form-grid">
						<div class="field full check-field">
							<label class="check-row" for="relay_upnp">
								<input id="relay_upnp" type="checkbox">
								<span>Enable UPnP port mapping</span>
							</label>
							<div class="hint">Recommended for home-hosted relays. Disable if your network is manually configured or managed externally.</div>
						</div>
						<div class="field">
							<label for="relay_service_tag">Service tag</label>
							<input id="relay_service_tag" placeholder="hornet-storage-service">
						</div>
						<div class="field">
							<label for="relay_secret_key">Shared secret</label>
							<input id="relay_secret_key" class="key-input" placeholder="generated automatically if left blank">
						</div>
						<div class="field">
							<label for="relay_public_key">Public key</label>
							<input id="relay_public_key" class="key-input" placeholder="leave blank to derive from the private key">
						</div>
						<div class="field">
							<label for="relay_dht_seed">DHT seed</label>
							<input id="relay_dht_seed" class="key-input" placeholder="leave blank to derive automatically">
						</div>
					</div>
				</section>

				<section class="subcard">
					<h3>Airlock Overrides</h3>
					<div class="form-grid">
						<div class="field">
							<label for="airlock_private_key">Airlock private key</label>
							<input id="airlock_private_key" class="key-input" placeholder="optional separate Airlock identity">
						</div>
						<div class="field">
							<label for="airlock_config_path">Airlock config path</label>
							<input id="airlock_config_path">
						</div>
						<div class="field">
							<label for="airlock_bind_address">Bind address</label>
							<input id="airlock_bind_address" placeholder="127.0.0.1">
						</div>
						<div class="field">
							<label for="airlock_port">Port</label>
							<input id="airlock_port" type="number" placeholder="11006">
						</div>
						<div class="field">
							<label for="airlock_relay">Relay address</label>
							<input id="airlock_relay" placeholder="127.0.0.1:11000">
						</div>
						<div class="field">
							<label for="airlock_repository_path">Repository path</label>
							<input id="airlock_repository_path" placeholder="repositories">
						</div>
						<div class="field">
							<label for="airlock_sidecar_address">Sidecar address</label>
							<input id="airlock_sidecar_address" placeholder="127.0.0.1:9100">
						</div>
						<div class="field">
							<label for="airlock_sidecar_mode">Sidecar mode</label>
							<select id="airlock_sidecar_mode">
								<option value="persistent">persistent</option>
								<option value="ephemeral">ephemeral</option>
							</select>
						</div>
						<div class="field full">
							<label for="airlock_sidecar_executable">Sidecar executable path</label>
							<input id="airlock_sidecar_executable" placeholder="leave blank for automatic discovery">
						</div>
					</div>
				</section>
			</div>

			<div class="advanced-grid">
				<div class="field full">
					<label for="relay_overrides">Relay JSON overrides</label>
					<textarea id="relay_overrides" placeholder='{"logging":{"level":"debug"}}'></textarea>
				</div>
				<div class="field full">
					<label for="airlock_overrides">Airlock JSON overrides</label>
					<textarea id="airlock_overrides" placeholder='{"sidecar":{"executable":"C:/Program Files/Hornet Storage/bin/hornets-hyperswarm.exe"}}'></textarea>
				</div>
			</div>

			<details class="preview">
				<summary>Preview full payload</summary>
				<textarea id="payload_preview"></textarea>
			</details>
		</details>
	</div>

	<script>
		const token = %q;
		const EXAMPLE_RELAY_PRIVATE_KEY = "c600149fe1207dd0cf5284d0a4bd767dc192181940d2a2b08f9571445f308a02";
		const EXAMPLE_RELAY_PUBLIC_KEY = "336b884334a2ad004b9b5c0d24ea727e0dfa9d9f6088d37386731611a2b38bcd";
		const EXAMPLE_RELAY_DHT_SEED = "";
		const EXAMPLE_RELAY_SECRET_KEY = "hornets-secret-key";
		const EXAMPLE_AIRLOCK_PRIVATE_KEY = "nsec1yas03jagdjsr8su00g92jurf7am3dldvu9tckyz796z8efpa594qp2nelz";
		let defaults = { relayConfig: {}, airlockConfig: {}, airlockConfigPath: "" };
		let generatedRelaySecret = "";
		let lockedRelayPrivateKey = "";
		let lockedRelayPrivateKeyDisplay = "";
		let lockedRelayOwnerPubkey = "";

		function randomHex(bytes) {
			const values = new Uint8Array(bytes);
			crypto.getRandomValues(values);
			return Array.from(values).map((value) => value.toString(16).padStart(2, "0")).join("");
		}

		function el(id) {
			return document.getElementById(id);
		}

		function sanitizeSeededValue(value, exampleValue) {
			const trimmed = String(value || "").trim();
			return trimmed === exampleValue ? "" : trimmed;
		}

		function setStatus(message, ok = true) {
			const status = el("status");
			status.textContent = message;
			status.className = ok ? "status ok" : "status bad";
		}

		function setReadOnlyValue(id, value, locked) {
			const input = el(id);
			if (!input) {
				return;
			}

			input.value = value;
			input.readOnly = locked;
			input.setAttribute("aria-readonly", locked ? "true" : "false");
		}

		function setRelayOwnerPubkeyValue(displayValue, hexValue, locked) {
			const input = el("relay_owner_pubkey");
			if (!input) {
				return;
			}

			input.dataset.hexValue = String(hexValue || "").trim();
			setReadOnlyValue("relay_owner_pubkey", displayValue, locked);
		}

		function applyRelayIdentityLock(privateKey, privateKeyDisplay, publicKey, publicKeyDisplay) {
			lockedRelayPrivateKey = String(privateKey || "").trim();
			lockedRelayPrivateKeyDisplay = String(privateKeyDisplay || lockedRelayPrivateKey).trim();
			lockedRelayOwnerPubkey = String(publicKey || "").trim();
			const ownerDisplayValue = String(publicKeyDisplay || lockedRelayOwnerPubkey).trim();

			if (!lockedRelayPrivateKey || !lockedRelayOwnerPubkey) {
				return;
			}

			setReadOnlyValue("relay_private_key", lockedRelayPrivateKeyDisplay || lockedRelayPrivateKey, true);
			setRelayOwnerPubkeyValue(ownerDisplayValue, lockedRelayOwnerPubkey, true);
			el("relay_public_key").value = "";
			el("relay_dht_seed").value = "";
			el("airlock_private_key").value = "";
			el("relay_private_key_lock_hint").hidden = false;
			el("relay_owner_pubkey_lock_hint").hidden = false;
			buildPayload();
			setStatus("Using the signed-in Nosis key for relay bootstrap.");
		}

		function deepMerge(target, src) {
			for (const [key, value] of Object.entries(src || {})) {
				if (value && typeof value === "object" && !Array.isArray(value)) {
					target[key] = deepMerge(target[key] && typeof target[key] === "object" ? target[key] : {}, value);
				} else {
					target[key] = value;
				}
			}
			return target;
		}

		function parseOverride(id) {
			const text = el(id).value.trim();
			if (!text) {
				return {};
			}
			try {
				const parsed = JSON.parse(text);
				if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
					throw new Error("must be a JSON object");
				}
				return parsed;
			} catch (error) {
				throw new Error(id + ": " + error.message);
			}
		}

		function syncAccessSettings(relay) {
			relay.allowed_users = relay.allowed_users || {};
			relay.allowed_users.mode = "invite-only";
			relay.allowed_users.read = "allowed_users";
			relay.allowed_users.write = "allowed_users";
		}

		function buildPayload() {
			const relay = JSON.parse(JSON.stringify(defaults.relayConfig || {}));
			const airlock = JSON.parse(JSON.stringify(defaults.airlockConfig || {}));

			relay.relay = relay.relay || {};
			relay.server = relay.server || {};
			airlock.sidecar = airlock.sidecar || {};

			const relayPrivateKey = lockedRelayPrivateKey || el("relay_private_key").value.trim();
			const generatedSecret = el("relay_secret_key").value.trim() || generatedRelaySecret || relay.relay.secret_key || "";

			relay.relay.name = el("relay_name").value.trim();
			relay.relay.contact = el("relay_contact").value.trim();
			relay.relay.description = el("relay_description").value.trim();
			relay.relay.icon = el("relay_icon").value.trim();
			relay.relay.service_tag = el("relay_service_tag").value.trim() || relay.relay.service_tag || "hornet-storage-service";
			relay.relay.private_key = relayPrivateKey;
			relay.relay.public_key = el("relay_public_key").value.trim();
			relay.relay.dht_seed = el("relay_dht_seed").value.trim();
			relay.relay.secret_key = generatedSecret;
			relay.server.upnp = Boolean(el("relay_upnp").checked);

			const airlockPort = el("airlock_port").value.trim();
			airlock.bind_address = el("airlock_bind_address").value.trim() || airlock.bind_address || "127.0.0.1";
			airlock.port = Number(airlockPort || airlock.port || 11006);
			airlock.relay = el("airlock_relay").value.trim() || airlock.relay || "127.0.0.1:11000";
			airlock.repository_path = el("airlock_repository_path").value.trim() || airlock.repository_path || "repositories";
			airlock.private_key = el("airlock_private_key").value.trim();
			airlock.sidecar.address = el("airlock_sidecar_address").value.trim() || airlock.sidecar.address || "127.0.0.1:9100";
			airlock.sidecar.mode = el("airlock_sidecar_mode").value.trim() || airlock.sidecar.mode || "persistent";
			airlock.sidecar.executable = el("airlock_sidecar_executable").value.trim() || airlock.sidecar.executable || "";

			deepMerge(relay, parseOverride("relay_overrides"));
			deepMerge(airlock, parseOverride("airlock_overrides"));
			syncAccessSettings(relay);

			const payload = {
				relayConfig: relay,
				airlockConfig: airlock,
				airlockConfigPath: el("airlock_config_path").value.trim() || defaults.airlockConfigPath || "",
				relayOwnerPubkey: el("relay_owner_pubkey").dataset.hexValue || el("relay_owner_pubkey").value.trim()
			};

			el("payload_preview").value = JSON.stringify(payload, null, 2);
			return payload;
		}

		async function request(path, method, body) {
			const res = await fetch(path, {
				method,
				headers: {
					"Content-Type": "application/json",
					"X-Setup-Token": token
				},
				body: body ? JSON.stringify(body) : undefined
			});
			const data = await res.json().catch(() => ({}));
			return { res, data };
		}

		async function loadDefaults() {
			if (window.self !== window.top) {
				document.body.classList.add("embedded");
			}

			const res = await fetch("/setup/defaults", { cache: "no-store" });
			defaults = await res.json();

			const relay = defaults.relayConfig?.relay || {};
			const server = defaults.relayConfig?.server || {};
			const allowedUsers = defaults.relayConfig?.allowed_users || {};
			const airlock = defaults.airlockConfig || {};

			generatedRelaySecret = sanitizeSeededValue(relay.secret_key, EXAMPLE_RELAY_SECRET_KEY) || randomHex(32);

			el("relay_name").value = relay.name || "Hornet Storage";
			el("relay_contact").value = relay.contact || "";
			el("relay_description").value = relay.description || "";
			el("relay_icon").value = relay.icon || "";
			el("relay_private_key").value = sanitizeSeededValue(relay.private_key, EXAMPLE_RELAY_PRIVATE_KEY);
			el("relay_service_tag").value = relay.service_tag || "hornet-storage-service";
			el("relay_public_key").value = sanitizeSeededValue(relay.public_key, EXAMPLE_RELAY_PUBLIC_KEY);
			el("relay_dht_seed").value = sanitizeSeededValue(relay.dht_seed || relay.dht_key, EXAMPLE_RELAY_DHT_SEED);
			el("relay_secret_key").value = generatedRelaySecret;
			el("relay_upnp").checked = typeof server.upnp === "boolean" ? server.upnp : true;
			setRelayOwnerPubkeyValue(
				sanitizeSeededValue(defaults.relayOwnerPubkey || relay.public_key, EXAMPLE_RELAY_PUBLIC_KEY),
				"",
				false
			);
			el("airlock_bind_address").value = airlock.bind_address || "127.0.0.1";
			el("airlock_port").value = String(airlock.port || 11006);
			el("airlock_relay").value = airlock.relay || "127.0.0.1:11000";
			el("airlock_repository_path").value = airlock.repository_path || "repositories";
			el("airlock_private_key").value = sanitizeSeededValue(airlock.private_key, EXAMPLE_AIRLOCK_PRIVATE_KEY);
			el("airlock_sidecar_address").value = airlock.sidecar?.address || "127.0.0.1:9100";
			el("airlock_sidecar_mode").value = airlock.sidecar?.mode || "persistent";
			el("airlock_sidecar_executable").value = airlock.sidecar?.executable || "";
			el("airlock_config_path").value = defaults.airlockConfigPath || "";

			buildPayload();
			setStatus("Defaults loaded. Add your relay identity and private key, then apply setup.");

			if (lockedRelayPrivateKey && lockedRelayOwnerPubkey) {
				applyRelayIdentityLock(lockedRelayPrivateKey, lockedRelayPrivateKeyDisplay, lockedRelayOwnerPubkey);
			}
		}

		async function validateSetup() {
			try {
				const payload = buildPayload();
				const { res, data } = await request("/setup/validate", "POST", payload);
				setStatus(JSON.stringify({ status: res.status, result: data }, null, 2), res.ok);
				return { ok: res.ok, payload, data };
			} catch (error) {
				setStatus(String(error), false);
				return { ok: false, payload: null, data: { error: String(error) } };
			}
		}

		async function applySetup() {
			try {
				setStatus("Validating setup before applying...", true);
				const validation = await validateSetup();
				if (!validation.ok || !validation.payload) {
					return;
				}

				const payload = validation.payload;
				const { res, data } = await request("/setup/apply", "POST", payload);
				setStatus(JSON.stringify({ status: res.status, result: data }, null, 2), res.ok);
				if (res.ok) {
					if (window.self !== window.top) {
						window.parent.postMessage({ type: "hornets-relay-setup-applied" }, "*");
					}
					setTimeout(() => {
						setStatus("Setup applied. Relay is transitioning to normal startup...", true);
					}, 500);
				}
			} catch (error) {
				setStatus(String(error), false);
			}
		}

		["input", "change"].forEach((eventName) => {
			document.addEventListener(eventName, () => {
				try {
					buildPayload();
				} catch {
					// Ignore partial form state while the user is typing.
				}
			});

			window.addEventListener("message", (event) => {
				if (window.self === window.top) {
					return;
				}

				if (event.data?.type !== "hornets-relay-setup-prefill") {
					return;
				}

				applyRelayIdentityLock(event.data.privateKey, event.data.privateKeyDisplay, event.data.publicKey, event.data.publicKeyDisplay);
			});
		});

		loadDefaults().catch((error) => setStatus("Failed loading defaults: " + error, false));
	</script>
</body>
</html>`, token)
}

// ErrSetupInterrupted is returned by RunBootstrapSetup when a shutdown was
// requested through the stop channel before setup completed. Callers treat
// it as a deliberate stop rather than a setup failure.
var ErrSetupInterrupted = errors.New("bootstrap setup interrupted")

// RunBootstrapSetup serves the first-time setup UI on host:port and blocks
// until the operator applies a configuration, the context is cancelled, or a
// shutdown is requested through stop. A nil stop channel disables
// caller-driven interruption.
func RunBootstrapSetup(ctx context.Context, host string, port int, profileName string, stop <-chan struct{}) error {
	if !needsBootstrapSetup() {
		logging.Info("Bootstrap setup already completed - continuing normal startup", nil)
		return nil
	}

	profile, err := parseBootstrapSetupProfile(profileName)
	if err != nil {
		return err
	}
	relaySessionDefaults := readPreferredYAMLMap("config.yaml", "config.example.yaml")
	session, err := newBootstrapSetupSession(profile, relaySessionDefaults)
	if err != nil {
		return err
	}

	token, err := setupToken()
	if err != nil {
		return fmt.Errorf("failed to generate bootstrap token: %w", err)
	}

	applyCh := make(chan struct{}, 1)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	mustAuth := func(c *fiber.Ctx) error {
		if c.Method() == fiber.MethodGet {
			return c.Next()
		}
		if c.Get("X-Setup-Token") != token {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid setup token"})
		}
		return c.Next()
	}

	app.Use(mustAuth)

	app.Get("/setup/defaults", func(c *fiber.Ctx) error {
		airlockConfigPath := defaultAirlockConfigPath()
		if profile == BootstrapSetupProfileOperator {
			airlockConfigPath = session.airlockConfigPath
		}
		relayDefaults := readPreferredYAMLMap("config.yaml", "config.example.yaml")
		airlockDefaults := readPreferredYAMLMap(airlockConfigPath, filepath.Join("..", "airlock", "config.example.yaml"))
		if profile == BootstrapSetupProfileOperator {
			prepareOperatorSetupDefaults(relayDefaults, airlockDefaults)
		}
		return c.JSON(fiber.Map{
			"profile":           profile,
			"relayConfig":       relayDefaults,
			"airlockConfig":     airlockDefaults,
			"airlockConfigPath": airlockConfigPath,
		})
	})

	app.Get("/", func(c *fiber.Ctx) error {
		if profile == BootstrapSetupProfileOperator {
			return c.Type("html").SendString(renderOperatorSetupPage(token))
		}
		return c.Type("html").SendString(renderBootstrapSetupPage(token))
	})

	app.Get("/setup", func(c *fiber.Ctx) error {
		return c.Redirect("/", fiber.StatusTemporaryRedirect)
	})

	app.Get("/setup/status", func(c *fiber.Ctx) error {
		_, relayCfgErr := os.Stat("config.yaml")
		airlockPath := defaultAirlockConfigPath()
		_, airlockCfgErr := os.Stat(airlockPath)
		return c.JSON(fiber.Map{
			"profile":               profile,
			"needs_setup":           needsBootstrapSetup(),
			"bootstrap_complete":    !needsBootstrapSetup(),
			"relay_config_exists":   relayCfgErr == nil,
			"airlock_config_exists": airlockCfgErr == nil,
		})
	})

	app.Post("/setup/validate", func(c *fiber.Ctx) error {
		var payload setupPayload
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid payload"})
		}
		if err := prepareBootstrapSetupPayloadForSession(&payload, session); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		allowedUsers, _ := payload.RelayConfig["allowed_users"].(map[string]interface{})

		return c.JSON(fiber.Map{
			"ok":                  true,
			"profile":             profile,
			"relay_config_keys":   len(payload.RelayConfig),
			"airlock_config_keys": len(payload.AirlockConfig),
			"access_mode":         allowedUsers["mode"],
			"relay_owner_pubkey":  payload.RelayOwnerPubkey,
		})
	})

	app.Post("/setup/apply", func(c *fiber.Ctx) error {
		var payload setupPayload
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid payload"})
		}
		if err := prepareBootstrapSetupPayloadForSession(&payload, session); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}

		if payload.RelayConfig != nil && len(payload.RelayConfig) > 0 {
			if err := writeYAMLAtomic("config.yaml", payload.RelayConfig); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
			}
		}

		if payload.AirlockConfig != nil && len(payload.AirlockConfig) > 0 {
			path := payload.AirlockConfigPath
			if path == "" {
				path = defaultAirlockConfigPath()
			}
			if err := writeYAMLAtomic(path, payload.AirlockConfig); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
			}
		}

		if err := persistBootstrapRelayOwner(payload.RelayConfig, payload.RelayOwnerPubkey); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}

		if err := writeSetupMarkerForConfig(payload.RelayConfig); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}

		select {
		case applyCh <- struct{}{}:
		default:
		}

		return c.JSON(fiber.Map{"ok": true, "message": "setup saved", "profile": profile})
	})

	app.Post("/setup/abort", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	addr := fmt.Sprintf("%s:%d", host, port)
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listen(addr)
	}()

	logging.Info("Bootstrap setup mode enabled - waiting for setup completion", map[string]interface{}{
		"addr":    addr,
		"profile": profile,
	})

	select {
	case <-ctx.Done():
		_ = app.Shutdown()
		return ctx.Err()
	case <-stop:
		_ = app.Shutdown()
		return ErrSetupInterrupted
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	case <-applyCh:
		_ = app.Shutdown()
		logging.Info("Bootstrap setup completed - continuing normal startup", map[string]interface{}{
			"profile": profile,
		})
		return nil
	}
}
