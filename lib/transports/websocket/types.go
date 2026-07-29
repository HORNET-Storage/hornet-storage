package websocket

import "time"

// TODO: maybe we should move this into a different package since we use it in the sync package as well
// It certainly shouldn't be here, that's for sure
type NIP11RelayInfo struct {
	Name            string           `json:"name,omitempty"`
	Description     string           `json:"description,omitempty"`
	Pubkey          string           `json:"pubkey,omitempty"`
	DHTPubkey       string           `json:"dht_pubkey,omitempty"`
	RelayMode       string           `json:"relay_mode,omitempty"`
	ReadAccess      string           `json:"read_access,omitempty"`
	WriteAccess     string           `json:"write_access,omitempty"`
	Contact         string           `json:"contact,omitempty"`
	Icon            string           `json:"icon,omitempty"`
	SupportedNIPs   []int            `json:"supported_nips,omitempty"`
	Software        string           `json:"software,omitempty"`
	Version         string           `json:"version,omitempty"`
	BasePort        int              `json:"base_port,omitempty"`        // Base port for service offset calculations
	Services        RelayServices    `json:"services,omitempty"`         // External/non-offset service endpoints
	HornetExtension *HornetExtension `json:"hornet_extension,omitempty"` // custom extension for p2p context
}

// Port offset constants from Nostr base port
// Clients should use these offsets to calculate service ports
const (
	PortOffsetNostr    = 0 // Base port (Nostr WebSocket)
	PortOffsetHornets  = 1 // HyperDHT for DAG transfers
	PortOffsetPanel    = 2 // HTTP admin panel
	PortOffsetReactDev = 3 // React development server
	PortOffsetWallet   = 4 // Wallet service
	PortOffsetBlossom  = 5 // Blossom media storage
)

// RelayServices is a dynamic map of service name to endpoint configuration
// Built-in services (hornets, panel, blossom) use fixed offsets from base_port
// External services like airlock are advertised here with their actual endpoints
type RelayServices map[string]*ServiceEndpoint

// ServiceEndpoint describes how to connect to a specific service
type ServiceEndpoint struct {
	Host      string `json:"host,omitempty"`       // Hostname/IP if different from relay (for external services)
	Port      int    `json:"port"`                 // Port number for the service
	Path      string `json:"path,omitempty"`       // Optional URL path (for HTTP services)
	Pubkey    string `json:"pubkey,omitempty"`     // secp256k1 public key
	DHTPubkey string `json:"dht_pubkey,omitempty"` // ed25519 public key for HyperDHT connections
}

type HornetExtension struct {
	RelayMode   string    `json:"relay_mode,omitempty"`
	ReadAccess  string    `json:"read_access,omitempty"`
	WriteAccess string    `json:"write_access,omitempty"`
	DHTPubkey   string    `json:"dht_pubkey"`
	Signature   string    `json:"signature"`
	LastUpdated time.Time `json:"last_updated"`
}
