// Configuration and settings types
package types

// Config represents the complete application configuration
type Config struct {
	Server               ServerConfig           `mapstructure:"server"`
	ExternalServices     ExternalServicesConfig `mapstructure:"external_services"`
	Logging              LoggingConfig          `mapstructure:"logging"`
	Relay                RelayConfig            `mapstructure:"relay"`
	ContentFiltering     ContentFilteringConfig `mapstructure:"content_filtering"`
	EventFiltering       EventFilteringConfig   `mapstructure:"event_filtering"`
	AllowedUsersSettings AllowedUsersSettings   `mapstructure:"allowed_users"`
	PushNotifications    PushNotificationConfig `mapstructure:"push_notifications"`
}

// ServerConfig holds server-related configuration
type ServerConfig struct {
	Port        int    `mapstructure:"port"`
	BindAddress string `mapstructure:"bind_address"`
	UPNP        bool   `mapstructure:"upnp"`
	Nostr       bool   `mapstructure:"nostr"`
	Hornets     bool   `mapstructure:"hornets"`
	Web         bool   `mapstructure:"web"`
	Demo        bool   `mapstructure:"demo"`
	Blossom     bool   `mapstructure:"blossom"`
	DataPath    string `mapstructure:"data_path"`
}

// ExternalServicesConfig holds external service configurations
type ExternalServicesConfig struct {
	Ollama    OllamaConfig    `mapstructure:"ollama"`
	Moderator ModeratorConfig `mapstructure:"moderator"`
	Wallet    WalletConfig    `mapstructure:"wallet"`
}

// OllamaConfig holds Ollama service configuration
type OllamaConfig struct {
	URL     string `mapstructure:"url"`
	Model   string `mapstructure:"model"`
	Timeout int    `mapstructure:"timeout"`
}

// ModeratorConfig holds moderator service configuration
type ModeratorConfig struct {
	URL string `mapstructure:"url"`
}

// WalletConfig holds wallet service configuration
type WalletConfig struct {
	URL  string `mapstructure:"url"`
	Key  string `mapstructure:"key"`
	Name string `mapstructure:"name"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
	Path   string `mapstructure:"path"`
}

// RelayConfig holds relay-specific configuration
type RelayConfig struct {
	Name          string `mapstructure:"name"`
	Description   string `mapstructure:"description"`
	Contact       string `mapstructure:"contact"`
	Icon          string `mapstructure:"icon"`
	Software      string `mapstructure:"software"`
	Version       string `mapstructure:"version"`
	ServiceTag    string `mapstructure:"service_tag"`
	SupportedNIPs []int  `mapstructure:"supported_nips"`
	SecretKey     string `mapstructure:"secret_key"`
	PrivateKey    string `mapstructure:"private_key"`
	PublicKey     string `mapstructure:"public_key"`
	DHTSeed       string `mapstructure:"dht_seed"`
	DHTPublicKey  string `mapstructure:"dht_public_key"`
	DHTPrivateKey string `mapstructure:"dht_private_key"`
}

// ContentFilteringConfig holds content filtering configuration
type ContentFilteringConfig struct {
	TextFilter      TextFilterConfig      `mapstructure:"text_filter"`
	ImageModeration ImageModerationConfig `mapstructure:"image_moderation"`
}

// TextFilterConfig holds text filtering configuration
type TextFilterConfig struct {
	Enabled             bool  `mapstructure:"enabled"`
	CacheSize           int   `mapstructure:"cache_size"`
	CacheTTLSeconds     int   `mapstructure:"cache_ttl_seconds"`
	FullTextSearchKinds []int `mapstructure:"full_text_search_kinds"`
	SearchPageSize      int   `mapstructure:"search_page_size"`
	MaxSearchCandidates int   `mapstructure:"max_search_candidates"`
}

// ImageModerationConfig holds image moderation configuration
type ImageModerationConfig struct {
	Enabled              bool    `mapstructure:"enabled"`
	Mode                 string  `mapstructure:"mode"`
	Threshold            float64 `mapstructure:"threshold"`
	TimeoutSeconds       int     `mapstructure:"timeout_seconds"`
	CheckIntervalSeconds int     `mapstructure:"check_interval_seconds"`
	Concurrency          int     `mapstructure:"concurrency"`
}

// EventFilteringConfig holds event filtering configuration
type EventFilteringConfig struct {
	AllowUnregisteredKinds bool                       `mapstructure:"allow_unregistered_kinds"`
	RegisteredKinds        []int                      `mapstructure:"registered_kinds"`
	ModerationMode         string                     `mapstructure:"moderation_mode"`
	KindWhitelist          []string                   `mapstructure:"kind_whitelist"`
	MediaDefinitions       map[string]MediaDefinition `mapstructure:"media_definitions"`
	DynamicKinds           DynamicKindsConfig         `mapstructure:"dynamic_kinds"`
	Protocols              ProtocolsConfig            `mapstructure:"protocols"`
}

// MediaDefinition holds configuration for a specific media type
type MediaDefinition struct {
	MimePatterns []string `mapstructure:"mime_patterns"`
	Extensions   []string `mapstructure:"extensions"`
	MaxSizeMB    int      `mapstructure:"max_size_mb"`
}

// DynamicKindsConfig holds dynamic kinds configuration
type DynamicKindsConfig struct {
	Enabled      bool  `mapstructure:"enabled"`
	AllowedKinds []int `mapstructure:"allowed_kinds"`
}

// ProtocolsConfig holds protocols configuration
type ProtocolsConfig struct {
	Enabled          bool     `mapstructure:"enabled"`
	AllowedProtocols []string `mapstructure:"allowed_protocols"`
}

// SubscriptionTiersConfig holds subscription tier configuration
type SubscriptionTiers struct {
	Tiers []SubscriptionTier `mapstructure:"tiers"`
}

// Validation constants for subscription tier limits
const (
	MinMonthlyLimitBytes = 1048576       // 1 MB minimum
	MaxMonthlyLimitBytes = 1099511627776 // 1 TB maximum
)

// SubscriptionTier holds subscription tier configuration
type SubscriptionTier struct {
	Name              string `mapstructure:"name" json:"name"`
	PriceSats         int    `mapstructure:"price_sats" json:"price_sats"`
	MonthlyLimitBytes int64  `mapstructure:"monthly_limit_bytes" json:"monthly_limit_bytes"`
	Unlimited         bool   `mapstructure:"unlimited" json:"unlimited"`
}

// AllowedUsersSettings represents the unified access control configuration
type AllowedUsersSettings struct {
	Mode                    string             `json:"mode" mapstructure:"mode"`   // only-me, invite-only, public, subscription
	Read                    string             `json:"read" mapstructure:"read"`   // all_users, paid_users, allowed_users, only-me
	Write                   string             `json:"write" mapstructure:"write"` // all_users, paid_users, allowed_users, only-me
	RepoAccessOverrideKinds []int              `json:"repo_access_override_kinds" mapstructure:"repo_access_override_kinds"`
	Tiers                   []SubscriptionTier `json:"tiers" mapstructure:"tiers"`
	LastUpdated             int64              `json:"last_updated" mapstructure:"last_updated"`
}
