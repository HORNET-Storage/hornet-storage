package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/HORNET-Storage/hornet-storage/lib/types"
)

var (
	// Cache the configuration after first load
	cachedConfig    atomic.Value // stores *types.Config
	configLoadOnce  sync.Once
	configLoadError error

	// Only protect write operations
	writeMutex sync.Mutex

	// Debounce timer for config file changes
	debounceTimer *time.Timer
	debounceMutex sync.Mutex
)

// InitConfig initializes the global viper configuration
func InitConfig() error {
	// Configuration file settings
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/app")
	viper.AddConfigPath("./config")

	// Environment variable settings
	viper.SetEnvPrefix("HORNETS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Set defaults (will check internally if config exists)
	setDefaults()

	// Try to read config file
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found, create it from example
			fmt.Println("No config.yaml found, creating from config.example.yaml...")

			// Try to read config.example.yaml first
			viper.SetConfigFile("config.example.yaml")
			if err := viper.ReadInConfig(); err != nil {
				// Example not found, fall back to defaults
				fmt.Printf("Warning: config.example.yaml not found (%v), using built-in defaults...\n", err)
				// Reset config name and create with defaults
				viper.SetConfigName("config")
				viper.SetConfigType("yaml")
				if err := viper.WriteConfigAs("config.yaml"); err != nil {
					return fmt.Errorf("failed to create default config: %w", err)
				}
			} else {
				// Successfully read example, write it as config.yaml
				if err := viper.WriteConfigAs("config.yaml"); err != nil {
					return fmt.Errorf("failed to create config from example: %w", err)
				}
				fmt.Println("Created config.yaml from config.example.yaml")
			}

			// Switch back to config.yaml and read it
			viper.SetConfigFile("config.yaml")
			if err := viper.ReadInConfig(); err != nil {
				return fmt.Errorf("failed to read created config: %w", err)
			}
		} else {
			return fmt.Errorf("error reading config file: %w", err)
		}
	} else {
		// Config file exists
		fmt.Println("Using existing config.yaml - preserving user configurations")
	}

	// Load initial configuration into cache
	if err := reloadConfigCache(); err != nil {
		return fmt.Errorf("failed to load initial config: %w", err)
	}

	// Watch for config file changes with debouncing
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		// Debounce file changes to avoid reading partial writes on slower machines
		debounceMutex.Lock()
		defer debounceMutex.Unlock()

		// Cancel any existing timer
		if debounceTimer != nil {
			debounceTimer.Stop()
		}

		// Set a new timer to reload config after 500ms of no changes
		debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
			log.Printf("Config file changed (debounced): %s", e.Name)
			writeMutex.Lock()
			defer writeMutex.Unlock()

			if err := reloadConfigCache(); err != nil {
				log.Printf("Error reloading config cache after file change: %v", err)
			} else {
				log.Printf("Config cache refreshed after file change")
			}
		})
	})

	return nil
}

func normalizeAllowedUsersConfigValues() {
	if strings.TrimSpace(viper.GetString("allowed_users.read")) == "" {
		legacyRead := strings.TrimSpace(viper.GetString("allowed_users.read_access.scope"))
		if legacyRead != "" {
			viper.Set("allowed_users.read", legacyRead)
		}
	}

	if strings.TrimSpace(viper.GetString("allowed_users.write")) == "" {
		legacyWrite := strings.TrimSpace(viper.GetString("allowed_users.write_access.scope"))
		if legacyWrite != "" {
			viper.Set("allowed_users.write", legacyWrite)
		}
	}

	normalizeAllowedUsersConfigKey("allowed_users.mode")
	normalizeAllowedUsersConfigKey("allowed_users.read")
	normalizeAllowedUsersConfigKey("allowed_users.write")
}

func normalizeAllowedUsersConfigKey(key string) {
	value := strings.TrimSpace(viper.GetString(key))
	if value == "" {
		return
	}

	normalized := normalizeAllowedUsersConfigValue(value)
	if normalized != value {
		viper.Set(key, normalized)
	}
}

func normalizeAllowedUsersConfigValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "only_me":
		return "only-me"
	case "invite_only":
		return "invite-only"
	default:
		return value
	}
}

func normalizeRelayDHTConfigValues() {
	if strings.TrimSpace(viper.GetString("relay.dht_seed")) == "" {
		legacyDHTSeed := strings.TrimSpace(viper.GetString("relay.dht_key"))
		if legacyDHTSeed != "" {
			viper.Set("relay.dht_seed", legacyDHTSeed)
		}
	}
}

func canonicalConfigKey(key string) string {
	switch key {
	case "relay.dht_key":
		return "relay.dht_seed"
	default:
		return key
	}
}

func legacyConfigAliasesForKey(key string) []string {
	switch key {
	case "relay.dht_seed":
		return []string{"relay.dht_key"}
	default:
		return nil
	}
}

var requiredOrganizationKinds = []int{39504, 39505, 39506}

func normalizeOrganizationKinds(config *types.Config) {
	registeredSet := make(map[int]struct{}, len(config.EventFiltering.RegisteredKinds))
	for _, kind := range config.EventFiltering.RegisteredKinds {
		registeredSet[kind] = struct{}{}
	}
	for _, kind := range requiredOrganizationKinds {
		if _, exists := registeredSet[kind]; !exists {
			config.EventFiltering.RegisteredKinds = append(config.EventFiltering.RegisteredKinds, kind)
			registeredSet[kind] = struct{}{}
		}
	}

	whitelistSet := make(map[string]struct{}, len(config.EventFiltering.KindWhitelist))
	for _, kind := range config.EventFiltering.KindWhitelist {
		whitelistSet[kind] = struct{}{}
	}
	for _, kind := range requiredOrganizationKinds {
		name := fmt.Sprintf("kind%d", kind)
		if _, exists := whitelistSet[name]; !exists {
			config.EventFiltering.KindWhitelist = append(config.EventFiltering.KindWhitelist, name)
			whitelistSet[name] = struct{}{}
		}
	}
}

// reloadConfigCache loads the configuration from viper into the cache
func reloadConfigCache() error {
	normalizeAllowedUsersConfigValues()
	normalizeRelayDHTConfigValues()

	config := &types.Config{}
	if err := viper.Unmarshal(config); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}
	normalizeOrganizationKinds(config)
	cachedConfig.Store(config)
	return nil
}

// GetConfig returns the cached configuration struct
// This is extremely fast as it only reads from atomic.Value
func GetConfig() (*types.Config, error) {
	// Try to get cached config
	if cfg := cachedConfig.Load(); cfg != nil {
		return cfg.(*types.Config), nil
	}

	// If not loaded yet, load it once
	configLoadOnce.Do(func() {
		configLoadError = reloadConfigCache()
	})

	if configLoadError != nil {
		return nil, configLoadError
	}

	cfg := cachedConfig.Load()
	if cfg == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}

	return cfg.(*types.Config), nil
}

// GetPort returns the port for a service from configuration
// Falls back to base port with offset calculation if service-specific port not configured
func GetPort(service string) int {
	// First try to get from explicit service configuration
	servicePort := viper.GetInt(fmt.Sprintf("server.services.%s.port", service))
	if servicePort > 0 {
		return servicePort
	}

	// Fallback to base port with offset calculation
	cfg, err := GetConfig()
	if err != nil || cfg.Server.Port == 0 {
		return 11000 // fallback
	}

	basePort := cfg.Server.Port
	// Port offsets from types.go:
	// PortOffsetNostr   = 0 // Base port (Nostr WebSocket)
	// PortOffsetHornets = 1 // libp2p/QUIC for DAG transfers
	// PortOffsetPanel   = 2 // HTTP admin panel
	// PortOffsetReactDev = 3 // React development server
	// PortOffsetWallet  = 4 // Wallet service
	// PortOffsetBlossom = 5 // Blossom media storage
	switch service {
	case "nostr":
		return basePort // offset 0
	case "hornets":
		return basePort + 1 // offset 1
	case "web", "panel":
		return basePort + 2 // offset 2
	case "react", "reactdev":
		return basePort + 3 // offset 3
	case "wallet":
		return basePort + 4 // offset 4
	case "blossom":
		return basePort + 5 // offset 5
	default:
		return basePort
	}
}

// IsEnabled checks if a service/feature is enabled
func IsEnabled(feature string) bool {
	cfg, err := GetConfig()
	if err != nil {
		return false
	}

	switch feature {
	case "demo":
		return cfg.Server.Demo
	case "web":
		return cfg.Server.Web
	case "nostr":
		return cfg.Server.Nostr
	case "hornets":
		return cfg.Server.Hornets
	case "blossom":
		return cfg.Server.Blossom
	default:
		// For other features, we need to check viper
		// This is rare, so the lock is acceptable
		writeMutex.Lock()
		defer writeMutex.Unlock()
		return viper.GetBool(feature)
	}
}

// GetDataDir returns the data directory path
func GetDataDir() string {
	cfg, err := GetConfig()
	if err != nil || cfg.Server.DataPath == "" {
		return "./data" // fallback
	}
	return cfg.Server.DataPath
}

// GetPath returns a path relative to the data directory
func GetPath(subPath string) string {
	return filepath.Join(GetDataDir(), subPath)
}

// safeWriteKeys performs a read-modify-write on the YAML config file,
// updating only the specified dot-path keys. This avoids the
// viper.WriteConfig() problem where viper dumps its entire (possibly
// incomplete) in-memory tree and nukes keys it doesn't know about.
//
// Must be called while writeMutex is held.
func safeWriteKeys(keys map[string]interface{}) error {
	configPath := viper.ConfigFileUsed()
	if configPath == "" {
		configPath = "config.yaml"
	}

	// Read existing file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse into ordered map
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// root is a Document node; its first child is the mapping
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("unexpected YAML structure")
	}
	mapping := root.Content[0]

	for dotPath, value := range keys {
		setYAMLNode(mapping, strings.Split(dotPath, "."), value)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("failed to marshal config YAML: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// safeRemoveKey removes a dot-path key from the YAML config file without
// rewriting the rest of the file. Must be called while writeMutex is held.
func safeRemoveKey(dotPath string) error {
	configPath := viper.ConfigFileUsed()
	if configPath == "" {
		configPath = "config.yaml"
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse config YAML: %w", err)
	}

	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("unexpected YAML structure")
	}

	removeYAMLNode(root.Content[0], strings.Split(dotPath, "."))

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("failed to marshal config YAML: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// removeYAMLNode removes a key from a yaml.Node mapping tree by dot-path segments.
func removeYAMLNode(mapping *yaml.Node, path []string) {
	if mapping.Kind != yaml.MappingNode || len(path) == 0 {
		return
	}

	key := path[0]
	rest := path[1:]

	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			if len(rest) == 0 {
				// Remove this key-value pair
				mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
				return
			}
			if mapping.Content[i+1].Kind == yaml.MappingNode {
				removeYAMLNode(mapping.Content[i+1], rest)
			}
			return
		}
	}
}

// setYAMLNode traverses/creates the path in a yaml.Node mapping tree and
// sets the leaf to the given value.
func setYAMLNode(mapping *yaml.Node, path []string, value interface{}) {
	if mapping.Kind != yaml.MappingNode {
		return
	}

	key := path[0]
	rest := path[1:]

	// Search existing keys
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			if len(rest) == 0 {
				// Leaf: replace value node
				mapping.Content[i+1] = valueToYAMLNode(value)
				return
			}
			// Recurse into sub-mapping
			if mapping.Content[i+1].Kind == yaml.MappingNode {
				setYAMLNode(mapping.Content[i+1], rest, value)
				return
			}
			// Exists but not a mapping — replace with one so we can nest
			newMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapping.Content[i+1] = newMap
			setYAMLNode(newMap, rest, value)
			return
		}
	}

	// Key not found — append
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	if len(rest) == 0 {
		mapping.Content = append(mapping.Content, keyNode, valueToYAMLNode(value))
	} else {
		newMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapping.Content = append(mapping.Content, keyNode, newMap)
		setYAMLNode(newMap, rest, value)
	}
}

// valueToYAMLNode converts a Go value into a yaml.Node scalar.
func valueToYAMLNode(v interface{}) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode}
	switch val := v.(type) {
	case string:
		n.Tag = "!!str"
		n.Value = val
	case int:
		n.Tag = "!!int"
		n.Value = strconv.Itoa(val)
	case int64:
		n.Tag = "!!int"
		n.Value = strconv.FormatInt(val, 10)
	case float64:
		if val == float64(int64(val)) {
			n.Tag = "!!int"
			n.Value = strconv.FormatInt(int64(val), 10)
		} else {
			n.Tag = "!!float"
			n.Value = strconv.FormatFloat(val, 'f', -1, 64)
		}
	case bool:
		n.Tag = "!!bool"
		n.Value = strconv.FormatBool(val)
	default:
		n.Tag = "!!str"
		n.Value = fmt.Sprintf("%v", val)
	}
	return n
}

// SaveConfig saves the current configuration to file.
// WARNING: This uses viper.WriteConfig() which rewrites the entire file.
// Prefer UpdateConfig or UpdateMultipleSections for surgical updates.
func SaveConfig() error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	err := viper.WriteConfig()
	if err != nil {
		return err
	}

	// Reload cache after save
	return reloadConfigCache()
}

// UpdateConfig updates a configuration value and optionally saves it
// Now with change detection to avoid unnecessary writes
func UpdateConfig(key string, value interface{}, save bool) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	canonicalKey := canonicalConfigKey(key)

	// Check if the value actually changed
	currentValue := viper.Get(canonicalKey)
	if isConfigValueEqual(currentValue, value) {
		log.Printf("No change for %s, skipping update", canonicalKey)
		return nil
	}

	log.Printf("Updating %s: %v -> %v", canonicalKey, currentValue, value)
	viper.Set(canonicalKey, value)

	if save {
		if err := safeWriteKeys(map[string]interface{}{canonicalKey: value}); err != nil {
			return err
		}

		for _, legacyKey := range legacyConfigAliasesForKey(canonicalKey) {
			if err := safeRemoveKey(legacyKey); err != nil {
				return err
			}
		}
	}

	// Reload cache after update
	return reloadConfigCache()
}

// UpdateMultipleSections updates multiple configuration sections intelligently
// Only saves once at the end, and only if there were changes
func UpdateMultipleSections(settings map[string]interface{}) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	changedKeys := make(map[string]interface{})
	keysToRemove := make(map[string]struct{})

	// Process each section
	for sectionName, sectionValue := range settings {
		sectionMap, ok := sectionValue.(map[string]interface{})
		if !ok {
			// Handle simple top-level values
			canonicalSectionName := canonicalConfigKey(sectionName)
			currentValue := viper.Get(canonicalSectionName)
			if !isConfigValueEqual(currentValue, sectionValue) {
				log.Printf("Updating %s: %v -> %v", canonicalSectionName, currentValue, sectionValue)
				viper.Set(canonicalSectionName, sectionValue)
				changedKeys[canonicalSectionName] = sectionValue
				for _, legacyKey := range legacyConfigAliasesForKey(canonicalSectionName) {
					keysToRemove[legacyKey] = struct{}{}
				}
			}
			continue
		}

		// Process section fields
		log.Printf("Processing section: %s", sectionName)
		for fieldName, fieldValue := range sectionMap {
			fullKey := sectionName + "." + fieldName

			// Handle nested sections
			if nestedMap, ok := fieldValue.(map[string]interface{}); ok {
				for nestedField, nestedValue := range nestedMap {
					nestedKey := canonicalConfigKey(fullKey + "." + nestedField)
					currentValue := viper.Get(nestedKey)
					if !isConfigValueEqual(currentValue, nestedValue) {
						log.Printf("  Updating %s: %v -> %v", nestedKey, currentValue, nestedValue)
						viper.Set(nestedKey, nestedValue)
						changedKeys[nestedKey] = nestedValue
						for _, legacyKey := range legacyConfigAliasesForKey(nestedKey) {
							keysToRemove[legacyKey] = struct{}{}
						}
					}
				}
			} else {
				canonicalKey := canonicalConfigKey(fullKey)
				currentValue := viper.Get(canonicalKey)
				if !isConfigValueEqual(currentValue, fieldValue) {
					log.Printf("  Updating %s: %v -> %v", canonicalKey, currentValue, fieldValue)
					viper.Set(canonicalKey, fieldValue)
					changedKeys[canonicalKey] = fieldValue
					for _, legacyKey := range legacyConfigAliasesForKey(canonicalKey) {
						keysToRemove[legacyKey] = struct{}{}
					}
				}
			}
		}
	}

	// Only save if there were changes
	if len(changedKeys) > 0 {
		log.Println("Saving configuration changes...")
		if err := safeWriteKeys(changedKeys); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		for keyToRemove := range keysToRemove {
			if err := safeRemoveKey(keyToRemove); err != nil {
				return fmt.Errorf("failed to clean up legacy config key %s: %w", keyToRemove, err)
			}
		}

		// Reload cache after save
		if err := reloadConfigCache(); err != nil {
			return fmt.Errorf("failed to reload cache: %w", err)
		}

		log.Println("Configuration saved successfully")
	} else {
		log.Println("No configuration changes detected, skipping save")
	}

	return nil
}

// isConfigValueEqual compares two configuration values for equality
// Handles type normalization for JSON numbers (float64 vs int)
func isConfigValueEqual(a, b interface{}) bool {
	// Handle nil cases
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Normalize and compare
	normalizedA := normalizeConfigValue(a)
	normalizedB := normalizeConfigValue(b)

	// Use string comparison as a simple but effective equality check
	return fmt.Sprintf("%v", normalizedA) == fmt.Sprintf("%v", normalizedB)
}

// normalizeConfigValue ensures consistent types for comparison
func normalizeConfigValue(v interface{}) interface{} {
	switch val := v.(type) {
	case float64:
		// JSON numbers come as float64, convert to int if whole number
		if val == float64(int(val)) {
			return int(val)
		}
		return val
	case []interface{}:
		// Normalize array elements
		normalized := make([]interface{}, len(val))
		for i, item := range val {
			normalized[i] = normalizeConfigValue(item)
		}
		return normalized
	case map[string]interface{}:
		// Normalize map values
		normalized := make(map[string]interface{})
		for k, v := range val {
			normalized[k] = normalizeConfigValue(v)
		}
		return normalized
	default:
		return v
	}
}

// RefreshConfig forces a reload of the configuration cache
// This should be called after external changes to the configuration (e.g., via web UI)
func RefreshConfig() error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	return reloadConfigCache()
}

// InitConfigForTesting initializes the configuration system for testing
// This should be called after setting up viper values manually in tests
// It loads the config into the cache without reading from a file
func InitConfigForTesting() {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	// Reset the sync.Once so we can reinitialize
	configLoadOnce = sync.Once{}
	configLoadError = nil

	// Load config into cache from current viper values
	if err := reloadConfigCache(); err != nil {
		log.Printf("Warning: failed to load test config into cache: %v", err)
	}
}

// GetAllowedUsersSettings returns the allowed users settings from cached config
func GetAllowedUsersSettings() (*types.AllowedUsersSettings, error) {
	cfg, err := GetConfig()
	if err != nil {
		return nil, err
	}
	return &cfg.AllowedUsersSettings, nil
}

// GetExternalURL returns external service URLs
func GetExternalURL(service string) string {
	cfg, err := GetConfig()
	if err != nil {
		return ""
	}

	switch service {
	case "ollama":
		return cfg.ExternalServices.Ollama.URL
	case "moderator":
		return cfg.ExternalServices.Moderator.URL
	case "wallet":
		return cfg.ExternalServices.Wallet.URL
	default:
		return ""
	}
}

// GenerateRandomAPIKey generates a random 32-byte hexadecimal key
func GenerateRandomAPIKey() (string, error) {
	bytes := make([]byte, 32) // 32 bytes = 256 bits
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GetNIPMappings returns the current NIP mappings from configuration
func GetNIPMappings() map[string]string {
	// NIP mappings are stored separately in viper, not in the Config struct
	// For read operations, we can safely read from viper with minimal locking
	writeMutex.Lock()
	defer writeMutex.Unlock()

	mappings := viper.GetStringMapString("nip_mappings")
	if mappings == nil {
		return make(map[string]string)
	}
	return mappings
}

// UpdateNIPMapping updates or adds a single NIP mapping
func UpdateNIPMapping(kind, nip string) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	mappings := viper.GetStringMapString("nip_mappings")
	if mappings == nil {
		mappings = make(map[string]string)
	}
	mappings[kind] = nip
	viper.Set("nip_mappings", mappings)

	if err := safeWriteKeys(map[string]interface{}{"nip_mappings." + kind: nip}); err != nil {
		return err
	}

	// Reload cache after update
	return reloadConfigCache()
}

// RemoveNIPMapping removes a NIP mapping for a specific kind
func RemoveNIPMapping(kind string) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	mappings := viper.GetStringMapString("nip_mappings")
	if mappings == nil {
		return nil // Nothing to remove
	}
	delete(mappings, kind)
	viper.Set("nip_mappings", mappings)

	// For removal we need to rewrite — use safeRemoveKey
	if err := safeRemoveKey("nip_mappings." + kind); err != nil {
		return err
	}

	// Reload cache after update
	return reloadConfigCache()
}

// RemoveConfigKey removes a single configuration key from the YAML file and cache.
func RemoveConfigKey(key string) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()

	if err := safeRemoveKey(key); err != nil {
		return err
	}

	if err := viper.ReadInConfig(); err != nil {
		return err
	}

	return reloadConfigCache()
}

// GetNIPForKind returns the NIP number for a given kind
func GetNIPForKind(kind int) (int, error) {
	kindStr := strconv.Itoa(kind)
	mappings := GetNIPMappings()

	nipStr, exists := mappings[kindStr]
	if !exists {
		return 0, fmt.Errorf("no NIP mapping found for kind %d", kind)
	}

	nip, err := strconv.Atoi(nipStr)
	if err != nil {
		return 0, fmt.Errorf("invalid NIP number for kind %d: %v", kind, err)
	}

	return nip, nil
}

// GetSupportedNIPsFromKinds returns unique NIP numbers for given kinds
func GetSupportedNIPsFromKinds(kinds []string) ([]int, error) {
	nipSet := make(map[int]struct{})

	// Always include system-critical NIPs
	systemCriticalKinds := []int{555, 10411, 11888}
	for _, kind := range systemCriticalKinds {
		if nip, err := GetNIPForKind(kind); err == nil {
			nipSet[nip] = struct{}{}
		}
	}

	// Process user-configured kinds
	for _, kindStr := range kinds {
		// Remove "kind" prefix if present
		kindStr = strings.TrimPrefix(kindStr, "kind")

		kind, err := strconv.Atoi(kindStr)
		if err != nil {
			log.Printf("Warning: Invalid kind number '%s': %v", kindStr, err)
			continue
		}

		nip, err := GetNIPForKind(kind)
		if err != nil {
			log.Printf("Warning: No NIP mapping found for kind %d: %v", kind, err)
			continue
		}

		nipSet[nip] = struct{}{}
	}

	// Convert set to sorted slice
	nips := make([]int, 0, len(nipSet))
	for nip := range nipSet {
		nips = append(nips, nip)
	}
	sort.Ints(nips)

	return nips, nil
}

// AddKindToNIPMapping adds or updates a kind-to-NIP mapping
func AddKindToNIPMapping(kind int, nip int) error {
	kindStr := strconv.Itoa(kind)
	nipStr := strconv.Itoa(nip)

	err := UpdateNIPMapping(kindStr, nipStr)
	if err != nil {
		return fmt.Errorf("failed to add kind-to-NIP mapping: %v", err)
	}

	log.Printf("Added kind-to-NIP mapping: kind=%d, nip=%d", kind, nip)

	return nil
}

// setDefaults sets all default values only if config doesn't exist
func setDefaults() {
	// Simply check if config.yaml exists in project root
	if _, err := os.Stat("config.yaml"); err == nil {
		// Config exists, don't set defaults
		fmt.Println("Config file exists, skipping defaults to preserve user settings")
		return
	}

	fmt.Println("No existing config found, setting defaults for new installation")

	// Server defaults
	viper.SetDefault("server.port", 11000)
	viper.SetDefault("server.bind_address", "0.0.0.0")
	viper.SetDefault("server.upnp", true)
	viper.SetDefault("server.nostr", true)
	viper.SetDefault("server.hornets", true)
	viper.SetDefault("server.web", true)
	viper.SetDefault("server.demo", false)
	viper.SetDefault("server.blossom", true)
	viper.SetDefault("server.data_path", "./data")

	// External services defaults
	viper.SetDefault("external_services.ollama.url", "http://ollama:11434")
	viper.SetDefault("external_services.ollama.model", "gemma2:2b")
	viper.SetDefault("external_services.ollama.timeout", 10000)
	viper.SetDefault("external_services.moderator.url", "http://moderator:8000")
	viper.SetDefault("external_services.wallet.url", "http://localhost:11003")
	viper.SetDefault("external_services.wallet.key", "")
	viper.SetDefault("external_services.wallet.name", "default")

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.output", "file")

	// Relay defaults
	viper.SetDefault("relay.name", "HORNETS")
	viper.SetDefault("relay.description", "HORNETS relay, the home of Nosis")
	viper.SetDefault("relay.contact", "support@hornetstorage.com")
	viper.SetDefault("relay.icon", "http://localhost:11002/logo.png")
	viper.SetDefault("relay.software", "HORNETS")
	viper.SetDefault("relay.version", "0.0.1")
	viper.SetDefault("relay.service_tag", "hornet-storage-service")
	viper.SetDefault("relay.supported_nips", []int{1, 2, 9, 11, 18, 23, 24, 25, 42, 45, 50, 51, 56, 57, 65, 116, 555, 888})
	viper.SetDefault("relay.secret_key", "hornets-secret-key")
	viper.SetDefault("relay.private_key", "")
	viper.SetDefault("relay.public_key", "")
	viper.SetDefault("relay.dht_seed", "")
	viper.SetDefault("relay.dht_public_key", "")
	viper.SetDefault("relay.dht_private_key", "")

	// Content filtering defaults
	viper.SetDefault("content_filtering.text_filter.enabled", true)
	viper.SetDefault("content_filtering.text_filter.cache_size", 10000)
	viper.SetDefault("content_filtering.text_filter.cache_ttl_seconds", 60)
	viper.SetDefault("content_filtering.text_filter.full_text_search_kinds", []int{1})

	viper.SetDefault("content_filtering.image_moderation.enabled", true)
	viper.SetDefault("content_filtering.image_moderation.mode", "full")
	viper.SetDefault("content_filtering.image_moderation.threshold", 0.4)
	viper.SetDefault("content_filtering.image_moderation.timeout_seconds", 600)
	viper.SetDefault("content_filtering.image_moderation.check_interval_seconds", 30)
	viper.SetDefault("content_filtering.image_moderation.concurrency", 5)

	// Event filtering defaults
	viper.SetDefault("event_filtering.allow_unregistered_kinds", false) // Default to false for security
	viper.SetDefault("event_filtering.registered_kinds", []int{
		0, 1, 3, 5, 6, 7, 8, // Basic kinds (NO kind 2, 4, or 16 handlers in main.go)
		72, 73, 74, 75, 76, 77, // Repository event kinds
		443, 444, 445, // MIP kinds (MLS group messaging)
		1059,                         // NIP-59 Gift Wrap (encrypted DMs)
		1063, 1111, 1808, 1809, 1984, // Special kinds (NO 1060 handler)
		6927, 7007, 9372, 9373, 9735, 9802, // Payment/Zap kinds (NO 9803 handler)
		10000, 10001, 10002, 10010, // List kinds
		10051,        // MIP-00 KeyPackage
		10411,        // Relay info kind (NO 10011 or 10022 handlers)
		11011,        // Relay list kind
		31415, 16630, // Parameterized replaceable kinds (repository permissions), branch metadata
		39504, 39505, 39506, // Organization definition, invitations, and responses
		19841, 19842, 19843, // Subscription kinds
		22242,               // Auth kind
		30000, 30008, 30009, // Parameterized replaceable kinds
		30023, 30078, 30079, 30301, 30302, // Long-form content kinds
		31416, // Release artifact sets (parameterized replaceable)
		30303, // Repository blacklist (parameterized replaceable)
	})
	viper.SetDefault("event_filtering.moderation_mode", "strict")
	viper.SetDefault("event_filtering.kind_whitelist", []string{"kind0", "kind1", "kind22242", "kind10010", "kind19841", "kind19842", "kind19843", "kind10002", "kind1111", "kind1808", "kind1809", "kind443", "kind444", "kind445", "kind1059", "kind10051", "kind72", "kind73", "kind74", "kind75", "kind76", "kind77", "kind6927", "kind7007", "kind31415", "kind16630", "kind31416", "kind30078", "kind30301", "kind30302", "kind30303", "kind39504", "kind39505", "kind39506"})
	viper.SetDefault("event_filtering.dynamic_kinds.enabled", false)
	viper.SetDefault("event_filtering.dynamic_kinds.allowed_kinds", []int{})
	viper.SetDefault("event_filtering.protocols.enabled", false)
	viper.SetDefault("event_filtering.protocols.allowed_protocols", []string{})

	// Media definitions defaults
	viper.SetDefault("event_filtering.media_definitions.image.mime_patterns", []string{"image/*"})
	viper.SetDefault("event_filtering.media_definitions.image.extensions", []string{".jpg", ".jpeg", ".png", ".gif", ".webp"})
	viper.SetDefault("event_filtering.media_definitions.image.max_size_mb", 100)

	viper.SetDefault("event_filtering.media_definitions.video.mime_patterns", []string{"video/*"})
	viper.SetDefault("event_filtering.media_definitions.video.extensions", []string{".mp4", ".webm", ".avi", ".mov"})
	viper.SetDefault("event_filtering.media_definitions.video.max_size_mb", 500)

	viper.SetDefault("event_filtering.media_definitions.audio.mime_patterns", []string{"audio/*"})
	viper.SetDefault("event_filtering.media_definitions.audio.extensions", []string{".mp3", ".wav", ".ogg", ".flac"})
	viper.SetDefault("event_filtering.media_definitions.audio.max_size_mb", 100)

	viper.SetDefault("event_filtering.media_definitions.git.mime_patterns", []string{"application/x-git"})
	viper.SetDefault("event_filtering.media_definitions.git.extensions", []string{".git", ".bundle", ".json"})
	viper.SetDefault("event_filtering.media_definitions.git.max_size_mb", 100)

	viper.SetDefault("event_filtering.media_definitions.binary.mime_patterns", []string{"application/octet-stream"})
	viper.SetDefault("event_filtering.media_definitions.binary.extensions", []string{".bin", ".dat", ".blob"})
	viper.SetDefault("event_filtering.media_definitions.binary.max_size_mb", 100)

	// Allowed users defaults - public relay for rapid testing
	viper.SetDefault("allowed_users.mode", "public")
	viper.SetDefault("allowed_users.read", "all_users")
	viper.SetDefault("allowed_users.write", "all_users")
	viper.SetDefault("allowed_users.repo_access_override_kinds", []int{72, 73, 74, 75, 76, 77, 1111, 6927, 7007, 31415, 16630, 31416, 30078, 30301, 30302, 30303})
	viper.SetDefault("allowed_users.last_updated", 0)
	viper.SetDefault("allowed_users.batch_update_on_startup", false) // Disable batch update by default for performance

	// Default free tier with 100MB monthly storage
	viper.SetDefault("allowed_users.tiers", []map[string]interface{}{
		{
			"name":                "Basic",
			"price_sats":          0,
			"monthly_limit_bytes": 104857600, // 100MB
			"unlimited":           false,
		},
	})

	// NIP mappings defaults
	viper.SetDefault("nip_mappings", map[string]string{
		// NIP-01: Basic Protocol
		"0": "1", // Profile metadata
		"1": "1", // Short text note
		"2": "1", // Recommend relay (deprecated)

		// NIP-02: Contact List
		"3": "2", // Contact list

		// NIP-09: Event Deletion
		"5": "9", // Deletion request

		// NIP-18: Reposts
		"6":  "18", // Repost
		"16": "18", // Generic repost

		// NIP-25: Reactions
		"7": "25", // Reaction

		// NIP-58: Badges
		"8":     "58", // Badge award
		"30008": "58", // Profile badge
		"30009": "58", // Badge definition

		// NIP-23: Long-form Content
		"30023": "23", // Long-form content

		// NIP-51: Lists
		"10000": "51", // Mute list
		"10001": "51", // Pin list
		"30000": "51", // Categorized people list

		// NIP-56: Reporting
		"1984": "56", // Reporting

		// NIP-57: Lightning Zaps
		"9735": "57", // Zap receipt

		// NIP-65: Relay List Metadata
		"10002": "65", // Relay list metadata

		// NIP-84: Highlights
		"9802": "84", // Highlight

		// NIP-116: Event Paths
		"30079": "116", // Event paths

		// NIP-117: Double Ratchet DM
		"1060": "117", // Message event

		// NIP-78: Application-specific Data
		"30078": "78", // Application-specific Data

		// NIP-59: Gift Wrap
		"1059": "59", // Gift wrap (encrypted DMs)

		// NIP-94: File Metadata
		"1063": "94", // File metadata

		// Custom HORNETS NIPs
		"10411": "888", // Subscription info
		"11888": "888", // Custom HORNETS protocol
		"11011": "888", // Custom HORNETS
		"555":   "555", // X-Nostr bridge

		// MIP kinds (MLS group messaging)
		"443":   "999", // MIP-00 KeyPackage
		"444":   "999", // MIP-02 Welcome Event
		"445":   "999", // MIP-03 Group Event
		"10051": "999", // MIP-00 KeyPackage Directory

		// Custom kinds
		"9372":  "888", // Custom application
		"9373":  "888", // Custom application
		"31415": "888", // Custom HORNETS (repository permissions, parameterized replaceable)
		"16630": "888", // Custom HORNETS (branch metadata)
		"31416": "888", // Release artifact sets
		"30303": "888", // Custom HORNETS (repository blacklist)
		"39504": "888", // Nosis organization definition
		"39505": "888", // Nosis organization invitation
		"39506": "888", // Nosis organization invitation response

		// Additional kinds
		"10010": "51",  // Additional list type
		"10011": "51",  // Additional list type
		"10022": "51",  // Additional list type
		"9803":  "84",  // Additional highlight type
		"22242": "42",  // Client Authentication
		"19841": "888", // Payment subscription
		"19842": "888", // Payment subscription
		"19843": "888", // Payment subscription
	})

	// Push notification defaults
	viper.SetDefault("push_notifications.enabled", false)

	// APNs Configuration defaults
	viper.SetDefault("push_notifications.apns.enabled", false)
	viper.SetDefault("push_notifications.apns.key_id", "")
	viper.SetDefault("push_notifications.apns.team_id", "")
	viper.SetDefault("push_notifications.apns.bundle_id", "")
	viper.SetDefault("push_notifications.apns.key_path", "")
	viper.SetDefault("push_notifications.apns.production", false)

	// FCM Configuration defaults
	viper.SetDefault("push_notifications.fcm.enabled", false)
	viper.SetDefault("push_notifications.fcm.project_id", "")
	viper.SetDefault("push_notifications.fcm.credentials_path", "")

	// Service Configuration defaults
	viper.SetDefault("push_notifications.service.queue_size", 1000)
	viper.SetDefault("push_notifications.service.worker_count", 10)
	viper.SetDefault("push_notifications.service.batch_size", 100)
	viper.SetDefault("push_notifications.service.retry_attempts", 3)
	viper.SetDefault("push_notifications.service.retry_delay", "5s")
}

// GetAllSettingsAsMap returns all configuration settings as a map
// This is a thread-safe alternative to viper.AllSettings() which can cause
// concurrent map read/write errors when viper.WatchConfig() is enabled
func GetAllSettingsAsMap() (map[string]interface{}, error) {
	// Get the cached config struct
	cfg, err := GetConfig()
	if err != nil {
		return nil, err
	}

	// Manually build the settings map using the original viper key structure
	// This ensures compatibility with existing code that expects specific key names
	settings := make(map[string]interface{})

	// Server settings
	settings["server"] = map[string]interface{}{
		"port":         cfg.Server.Port,
		"bind_address": cfg.Server.BindAddress,
		"upnp":         cfg.Server.UPNP,
		"nostr":        cfg.Server.Nostr,
		"hornets":      cfg.Server.Hornets,
		"web":          cfg.Server.Web,
		"demo":         cfg.Server.Demo,
		"data_path":    cfg.Server.DataPath,
	}

	// External services settings
	settings["external_services"] = map[string]interface{}{
		"ollama": map[string]interface{}{
			"url":     cfg.ExternalServices.Ollama.URL,
			"model":   cfg.ExternalServices.Ollama.Model,
			"timeout": cfg.ExternalServices.Ollama.Timeout,
		},
		"moderator": map[string]interface{}{
			"url": cfg.ExternalServices.Moderator.URL,
		},
		"wallet": map[string]interface{}{
			"url":  cfg.ExternalServices.Wallet.URL,
			"key":  cfg.ExternalServices.Wallet.Key,
			"name": cfg.ExternalServices.Wallet.Name,
		},
	}

	// Logging settings
	settings["logging"] = map[string]interface{}{
		"level":  cfg.Logging.Level,
		"format": cfg.Logging.Format,
		"output": cfg.Logging.Output,
		"path":   cfg.Logging.Path,
	}

	// Relay settings
	settings["relay"] = map[string]interface{}{
		"name":            cfg.Relay.Name,
		"description":     cfg.Relay.Description,
		"contact":         cfg.Relay.Contact,
		"icon":            cfg.Relay.Icon,
		"software":        cfg.Relay.Software,
		"version":         cfg.Relay.Version,
		"service_tag":     cfg.Relay.ServiceTag,
		"supported_nips":  cfg.Relay.SupportedNIPs,
		"secret_key":      cfg.Relay.SecretKey,
		"private_key":     cfg.Relay.PrivateKey,
		"public_key":      cfg.Relay.PublicKey,
		"dht_seed":        cfg.Relay.DHTSeed,
		"dht_public_key":  cfg.Relay.DHTPublicKey,
		"dht_private_key": cfg.Relay.DHTPrivateKey,
		"dht_key":         cfg.Relay.DHTSeed,
	}

	// Content filtering settings
	settings["content_filtering"] = map[string]interface{}{
		"text_filter": map[string]interface{}{
			"enabled":                cfg.ContentFiltering.TextFilter.Enabled,
			"cache_size":             cfg.ContentFiltering.TextFilter.CacheSize,
			"cache_ttl_seconds":      cfg.ContentFiltering.TextFilter.CacheTTLSeconds,
			"full_text_search_kinds": cfg.ContentFiltering.TextFilter.FullTextSearchKinds,
		},
		"image_moderation": map[string]interface{}{
			"enabled":                cfg.ContentFiltering.ImageModeration.Enabled,
			"mode":                   cfg.ContentFiltering.ImageModeration.Mode,
			"threshold":              cfg.ContentFiltering.ImageModeration.Threshold,
			"timeout_seconds":        cfg.ContentFiltering.ImageModeration.TimeoutSeconds,
			"check_interval_seconds": cfg.ContentFiltering.ImageModeration.CheckIntervalSeconds,
			"concurrency":            cfg.ContentFiltering.ImageModeration.Concurrency,
		},
	}

	// Event filtering settings
	settings["event_filtering"] = map[string]interface{}{
		"allow_unregistered_kinds": cfg.EventFiltering.AllowUnregisteredKinds,
		"registered_kinds":         cfg.EventFiltering.RegisteredKinds,
		"moderation_mode":          cfg.EventFiltering.ModerationMode,
		"kind_whitelist":           cfg.EventFiltering.KindWhitelist,
		"media_definitions":        cfg.EventFiltering.MediaDefinitions,
		"dynamic_kinds": map[string]interface{}{
			"enabled":       cfg.EventFiltering.DynamicKinds.Enabled,
			"allowed_kinds": cfg.EventFiltering.DynamicKinds.AllowedKinds,
		},
		"protocols": map[string]interface{}{
			"enabled":           cfg.EventFiltering.Protocols.Enabled,
			"allowed_protocols": cfg.EventFiltering.Protocols.AllowedProtocols,
		},
	}

	// Allowed users settings
	settings["allowed_users"] = map[string]interface{}{
		"mode":                       cfg.AllowedUsersSettings.Mode,
		"read":                       cfg.AllowedUsersSettings.Read,
		"write":                      cfg.AllowedUsersSettings.Write,
		"repo_access_override_kinds": cfg.AllowedUsersSettings.RepoAccessOverrideKinds,
		"tiers":                      cfg.AllowedUsersSettings.Tiers,
		"last_updated":               cfg.AllowedUsersSettings.LastUpdated,
	}

	// Push notifications settings
	settings["push_notifications"] = map[string]interface{}{
		"enabled": cfg.PushNotifications.Enabled,
		"apns": map[string]interface{}{
			"enabled":    cfg.PushNotifications.APNS.Enabled,
			"key_id":     cfg.PushNotifications.APNS.KeyID,
			"team_id":    cfg.PushNotifications.APNS.TeamID,
			"bundle_id":  cfg.PushNotifications.APNS.BundleID,
			"key_path":   cfg.PushNotifications.APNS.KeyPath,
			"production": cfg.PushNotifications.APNS.Production,
		},
		"fcm": map[string]interface{}{
			"enabled":          cfg.PushNotifications.FCM.Enabled,
			"project_id":       cfg.PushNotifications.FCM.ProjectID,
			"credentials_path": cfg.PushNotifications.FCM.CredentialsPath,
		},
		"service": map[string]interface{}{
			"queue_size":     cfg.PushNotifications.Service.QueueSize,
			"worker_count":   cfg.PushNotifications.Service.WorkerCount,
			"batch_size":     cfg.PushNotifications.Service.BatchSize,
			"retry_attempts": cfg.PushNotifications.Service.RetryAttempts,
			"retry_delay":    cfg.PushNotifications.Service.RetryDelay,
		},
	}

	// Add NIP mappings separately as they're not in the Config struct
	settings["nip_mappings"] = GetNIPMappings()

	// Note: There is no "subscriptions" section in the config - this was likely an old/unused key
	// that the frontend was checking for but doesn't exist in the actual configuration

	return settings, nil
}

// GetSettingValue returns a specific setting value by key path (e.g., "relay.name")
// This is a thread-safe alternative to viper.Get()
func GetSettingValue(key string) (interface{}, error) {
	settings, err := GetAllSettingsAsMap()
	if err != nil {
		return nil, err
	}

	// Navigate through nested keys
	keys := strings.Split(key, ".")
	var value interface{} = settings

	for _, k := range keys {
		if m, ok := value.(map[string]interface{}); ok {
			value = m[k]
		} else {
			return nil, fmt.Errorf("key not found: %s", key)
		}
	}

	return value, nil
}
