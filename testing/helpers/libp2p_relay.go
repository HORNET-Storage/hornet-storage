// Package helpers provides utilities for integration testing the HORNETS relay
package helpers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	"github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr"
	hsListener "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr/hyperswarm"
	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/config"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/download"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/query"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/upload"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/sidecar"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/spf13/viper"
)

// TestLibp2pRelay represents a test relay instance with hyperswarm support
type TestLibp2pRelay struct {
	Store        *badgerhold.BadgerholdStore
	Listener     *hsListener.HyperswarmListener
	DHTPublicKey string
	DataDir      string
	PrivateKey   string
	PublicKey    string
	mu           sync.Mutex
	running      bool
}

// TestLibp2pRelayConfig holds configuration for a test relay
type TestLibp2pRelayConfig struct {
	DataDir    string
	PrivateKey string // Optional - will generate if empty
}

// DefaultTestLibp2pConfig returns a default test configuration
func DefaultTestLibp2pConfig() TestLibp2pRelayConfig {
	return TestLibp2pRelayConfig{
		DataDir:    "",
		PrivateKey: "",
	}
}

// NewTestLibp2pRelay creates and starts a new test relay
func NewTestLibp2pRelay(cfg TestLibp2pRelayConfig) (*TestLibp2pRelay, error) {
	// Create temp directory if not specified
	dataDir := cfg.DataDir
	if dataDir == "" {
		var err error
		dataDir, err = os.MkdirTemp("", "hornet-test-relay-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp directory: %w", err)
		}
	}

	// Initialize config
	initTestLibp2pConfig(dataDir, cfg)

	// Initialize logging (suppress for tests)
	logging.InitLogger()

	// Initialize store
	storePath := filepath.Join(dataDir, "store")
	statsPath := filepath.Join(dataDir, "stats.db")
	store, err := badgerhold.InitStore(storePath, statsPath)
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("failed to initialize store: %w", err)
	}

	// Get or generate private key
	privateKeyStr := cfg.PrivateKey
	if privateKeyStr == "" {
		privateKeyStr = viper.GetString("relay.private_key")
	}

	// Deserialize private key for public key extraction
	_, publicKey, err := signing.DeserializePrivateKey(privateKeyStr)
	if err != nil {
		os.RemoveAll(dataDir)
		store.Cleanup()
		return nil, fmt.Errorf("failed to deserialize private key: %w", err)
	}

	serializedPublicKey, err := signing.SerializePublicKey(publicKey)
	if err != nil {
		os.RemoveAll(dataDir)
		store.Cleanup()
		return nil, fmt.Errorf("failed to serialize public key: %w", err)
	}

	// Create hyperswarm listener via sidecar
	hsClient, err := sidecar.GetClient()
	if err != nil {
		os.RemoveAll(dataDir)
		store.Cleanup()
		return nil, fmt.Errorf("failed to connect to hyperswarm sidecar: %w", err)
	}
	listener := hsListener.NewHyperswarmListener(hsClient)

	dhtKey := viper.GetString("relay.dht_seed")
	_, dhtPublicKey, err := listener.CreateServerFromSeed(dhtKey)
	if err != nil {
		os.RemoveAll(dataDir)
		store.Cleanup()
		return nil, fmt.Errorf("failed to create HyperDHT server: %w", err)
	}

	relay := &TestLibp2pRelay{
		Store:        store,
		Listener:     listener,
		DHTPublicKey: dhtPublicKey,
		DataDir:      dataDir,
		PrivateKey:   privateKeyStr,
		PublicKey:    *serializedPublicKey,
	}

	// Register DAG handlers
	if err := relay.registerHandlers(); err != nil {
		listener.Close()
		store.Cleanup()
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("failed to register handlers: %w", err)
	}

	relay.running = true
	return relay, nil
}

// registerHandlers registers the DAG handlers on the hyperswarm listener
func (r *TestLibp2pRelay) registerHandlers() error {
	canUpload := func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		return true
	}

	handleReceived := func(dag *merkle_dag.Dag, pubKey *string) {}

	canDownload := func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		return true
	}

	upload.AddUploadHandler(r.Listener, r.Store, canUpload, handleReceived)
	download.AddDownloadHandler(r.Listener, r.Store, canDownload)
	query.AddQueryHandler(r.Listener, r.Store)

	return nil
}

// Stop stops the test relay and cleans up resources
func (r *TestLibp2pRelay) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running {
		return nil
	}

	// Close hyperswarm listener
	if r.Listener != nil {
		if err := r.Listener.Close(); err != nil {
			logging.Errorf("Error closing hyperswarm listener: %v", err)
		}
	}

	// Close database
	if r.Store != nil {
		if err := r.Store.Cleanup(); err != nil {
			logging.Errorf("Error cleaning up store: %v", err)
		}
	}

	r.running = false
	return nil
}

// Cleanup stops the relay and removes all test data
func (r *TestLibp2pRelay) Cleanup() error {
	if err := r.Stop(); err != nil {
		return err
	}

	if r.DataDir != "" {
		if err := os.RemoveAll(r.DataDir); err != nil {
			return fmt.Errorf("failed to remove test data: %w", err)
		}
	}

	return nil
}

// WaitForReady waits for the relay to be ready to accept connections
func (r *TestLibp2pRelay) WaitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.DHTPublicKey != "" {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for relay to be ready")
}

// GetConnectionManager creates a client connection to this test relay through the
// same hyperswarm sidecar used by the listener.
func (r *TestLibp2pRelay) GetConnectionManager(ctx context.Context) (connmgr.ConnectionManager, error) {
	hsClient, err := sidecar.GetClient()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to hyperswarm sidecar: %w", err)
	}

	manager := connmgr.NewGenericConnectionManager()
	if err := manager.ConnectWithHyperswarm(ctx, "test-relay", r.DHTPublicKey, hsClient); err != nil {
		return nil, fmt.Errorf("failed to connect to test relay: %w", err)
	}
	return manager, nil
}

// initTestLibp2pConfig initializes viper config for testing
func initTestLibp2pConfig(dataDir string, cfg TestLibp2pRelayConfig) {
	viper.Reset()

	viper.Set("server.data_path", dataDir)

	if cfg.PrivateKey == "" {
		viper.Set("relay.private_key", "nsec1vl029mgpspedva04g90vltkh6fvh240zqtv9k0t9af8935ke9laqsnlfe5")
	} else {
		viper.Set("relay.private_key", cfg.PrivateKey)
	}

	viper.Set("relay.dht_seed", "c600149fe1207dd0cf5284d0a4bd767dc192181940d2a2b08f9571445f308a02")

	viper.Set("upload.enabled_uploads", []string{"all"})
	viper.Set("content_filtering.image_moderation.enabled", false)

	viper.Set("logging.level", "error")
	viper.Set("logging.output", "stdout")

	config.InitConfigForTesting()
}
