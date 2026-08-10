package types

import "time"

// PushDevice represents a registered device for push notifications
type PushDevice struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Pubkey        string    `gorm:"size:64;not null;index" json:"pubkey"`
	DeviceToken   string    `gorm:"size:255;not null" json:"device_token"`
	Platform      string    `gorm:"size:10;not null" json:"platform"` // 'ios' or 'android'
	AppIdentifier string    `gorm:"size:255" json:"app_identifier"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	IsActive      bool      `gorm:"default:true" json:"is_active"`
}

// PushNotificationLog represents a log entry for sent push notifications
type PushNotificationLog struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Pubkey           string     `gorm:"size:64;not null;index" json:"pubkey"`
	EventID          string     `gorm:"size:64;not null" json:"event_id"`
	EventKind        int        `gorm:"not null" json:"event_kind"`
	NotificationType string     `gorm:"size:50" json:"notification_type"`
	DeviceToken      string     `gorm:"size:255" json:"device_token"`
	Platform         string     `gorm:"size:10" json:"platform"`
	SentAt           *time.Time `json:"sent_at"`
	Delivered        bool       `gorm:"default:false" json:"delivered"`
	ErrorMessage     string     `gorm:"type:text" json:"error_message"`
	CreatedAt        time.Time  `gorm:"autoCreateTime" json:"created_at"`
}

// PushNotificationConfig holds push notification service configuration
type PushNotificationConfig struct {
	Enabled bool              `mapstructure:"enabled"`
	APNS    APNSConfig        `mapstructure:"apns"`
	FCM     FCMConfig         `mapstructure:"fcm"`
	Service PushServiceConfig `mapstructure:"service"`
}

// APNSConfig holds Apple Push Notification Service configuration
type APNSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	KeyID      string `mapstructure:"key_id"`
	TeamID     string `mapstructure:"team_id"`
	BundleID   string `mapstructure:"bundle_id"`
	KeyPath    string `mapstructure:"key_path"`
	Production bool   `mapstructure:"production"`
}

// FCMConfig holds Firebase Cloud Messaging configuration
type FCMConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	ProjectID       string `mapstructure:"project_id"`
	CredentialsPath string `mapstructure:"credentials_path"`
}

// PushServiceConfig holds general service configuration
type PushServiceConfig struct {
	QueueSize     int    `mapstructure:"queue_size"`
	WorkerCount   int    `mapstructure:"worker_count"`
	BatchSize     int    `mapstructure:"batch_size"`
	RetryAttempts int    `mapstructure:"retry_attempts"`
	RetryDelay    string `mapstructure:"retry_delay"`
	FollowGated     bool   `mapstructure:"follow_gated"`
	FollowCacheSize int    `mapstructure:"follow_cache_size"`
	FollowCacheTTL  string `mapstructure:"follow_cache_ttl"`
}
