package core

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof" // Import pprof for memory profiling
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	hsListener "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr/hyperswarm"
	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/config"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/sidecar"
	"github.com/HORNET-Storage/hornet-storage/services/push"
	hsClient "github.com/hornet-storage/hornets-hyperswarm/clients/go/hyperswarm"

	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"

	"github.com/HORNET-Storage/hornet-storage/lib/moderation"
	"github.com/HORNET-Storage/hornet-storage/lib/subscription"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
	"github.com/HORNET-Storage/hornet-storage/lib/wot"

	"github.com/HORNET-Storage/hornet-storage/lib/upnp"

	"github.com/spf13/viper"

	"github.com/HORNET-Storage/hornet-storage/lib/web"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/auth"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/count"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/filter"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1809"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind0"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10000"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10001"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10002"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10010"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10051"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10411"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1059"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1063"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind11011"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind16629"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind16630"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1808"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1984"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind19841"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind19842"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind19843"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind3"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30000"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30008"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30009"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30023"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30078"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30079"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind443"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind444"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind445"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind5"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind6"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind7"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind72"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind77"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind8"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9372"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9373"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9735"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9802"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/universal"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/claim"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/download"
	nostr_relay "github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/nostr_relay"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/query"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/services"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/upload"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	synckeys "github.com/HORNET-Storage/hornet-storage/lib/sync"
	ws "github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
)

// Options configures a single relay run.
type Options struct {
	// CompactDB runs database compaction to reclaim any potential disk space
	// before starting regular services.
	CompactDB bool
	// MemoryProfiler runs the pprof memory profiler enabling memory usage
	// debugging.
	MemoryProfiler bool
	// BootstrapSetup runs the first-time setup server before starting relay
	// services.
	BootstrapSetup bool
	// SetupHost is the host/interface for the first-time setup server.
	SetupHost string
	// SetupPort is the port for the first-time setup server.
	SetupPort int
	// SetupProfile selects the server-owned bootstrap policy. Empty keeps the
	// established Nosis desktop/local-relay behavior.
	SetupProfile string
	// Stop triggers a graceful shutdown when it is closed. Console
	// entrypoints wire it to SIGINT/SIGTERM; the Windows service entrypoint
	// wires it to SCM stop requests. A nil channel disables caller-driven
	// shutdown.
	Stop <-chan struct{}
}

// Initialize prepares the relay's process-wide configuration and logging state.
// UPnP is initialized inside Run only after first-time setup has applied and the
// final configuration has been reloaded.
func Initialize() {
	// Initialze config system
	err := config.InitConfig()
	if err != nil {
		logging.Fatalf("Failed to initialize config: %v", err)
	}

	// Initialize logging system
	if err := logging.InitLogger(); err != nil {
		logging.Fatalf("Failed to initialize logger: %v", err)
	}

	// Now use the logging system
	logging.Info("HORNETS Nostr Relay starting up", map[string]interface{}{
		"version": viper.GetString("relay.version"),
		"name":    viper.GetString("relay.name"),
	})
}

func initializeUPnP() {
	if !viper.GetBool("server.upnp") {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	upnpManager, err := upnp.Init(ctx)
	if err != nil {
		logging.Error("UPnP init failed", map[string]interface{}{
			"error": err,
		})
		return
	}
	if upnpManager == nil {
		logging.Error("UPnP init failed", map[string]interface{}{
			"error": "no UPnP router discovered",
		})
		return
	}

	ip, err := upnpManager.ExternalIP()
	if err == nil {
		logging.Info("UPnP External IP", map[string]interface{}{
			"ip": ip,
		})
	} else {
		logging.Error("Failed to get UPnP External IP", map[string]interface{}{
			"error": err,
		})
	}
}

const airlockRelayIdentityDomain = "hornets-airlock-dht-v1\x00"

func deriveAirlockDHTPublicKeyFromPrivateKey(privateKey string, domainSeparated bool) (string, error) {
	privateKeyBytes, err := signing.DecodeKey(strings.TrimSpace(privateKey))
	if err != nil {
		return "", fmt.Errorf("invalid Airlock private key: %w", err)
	}

	if len(privateKeyBytes) != ed25519.SeedSize {
		return "", fmt.Errorf("invalid Airlock private key length: expected %d bytes, got %d", ed25519.SeedSize, len(privateKeyBytes))
	}

	dhtSeed := privateKeyBytes
	if domainSeparated {
		material := make([]byte, 0, len(airlockRelayIdentityDomain)+len(privateKeyBytes))
		material = append(material, airlockRelayIdentityDomain...)
		material = append(material, privateKeyBytes...)
		digest := sha256.Sum256(material)
		dhtSeed = digest[:]
	}

	ed25519PrivateKey := ed25519.NewKeyFromSeed(dhtSeed)
	ed25519PublicKey := ed25519PrivateKey.Public().(ed25519.PublicKey)
	return hex.EncodeToString(ed25519PublicKey), nil
}

func syncAirlockServiceDHTPubkey() (string, error) {
	airlockConfigPath := defaultAirlockConfigPath()
	airlockConfig := readYAMLMap(airlockConfigPath)

	privateKey := stringSetting(airlockConfig["private_key"])
	domainSeparated := false
	if privateKey == "" {
		// The default deployment deliberately avoids duplicating the relay secret.
		// Derive a separate Airlock DHT seed so raw relay key bytes are never reused
		// across the relay's secp256k1 and Airlock's ed25519 contexts.
		privateKey = strings.TrimSpace(viper.GetString("relay.private_key"))
		domainSeparated = true
	}
	if privateKey == "" {
		return "", fmt.Errorf("Airlock private_key is empty in %s and relay.private_key is unavailable", airlockConfigPath)
	}

	return deriveAirlockDHTPublicKeyFromPrivateKey(privateKey, domainSeparated)
}

func forwardSidecarDHTPort(client *hsClient.Client) func() {
	if !viper.GetBool("server.upnp") {
		return func() {}
	}

	upnpManager := upnp.Get()
	if upnpManager == nil {
		logging.Warn("UPnP is enabled but no router was discovered for HyperDHT port mapping", nil)
		return func() {}
	}

	status, err := client.Status()
	if err != nil {
		logging.Error("Failed to read sidecar status for HyperDHT UPnP mapping", map[string]interface{}{
			"error": err,
		})
		return func() {}
	}
	if status == nil || status.DHT == nil || status.DHT.Port <= 0 || status.DHT.Port > 65535 {
		logging.Warn("Sidecar HyperDHT port unavailable for UPnP mapping", map[string]interface{}{
			"status": status,
		})
		return func() {}
	}

	port := uint16(status.DHT.Port)
	if err := upnpManager.ForwardPort(port, "Hornet Storage HyperDHT"); err != nil {
		logging.Error("Failed to forward HyperDHT port using UPnP", map[string]interface{}{
			"port":  port,
			"error": err,
		})
		return func() {}
	}

	logging.Info("Forwarded HyperDHT port using UPnP", map[string]interface{}{
		"port": port,
	})

	return func() {
		upnpManager.RemovePort(port)
	}
}

// Run executes the full relay lifecycle and blocks until the relay stops.
// It preserves the startup sequence and behavior of the original
// services/server/port entrypoint. Fatal startup errors still terminate the
// process through logging.Fatal exactly as before. A caller-requested stop
// via opts.Stop performs the same graceful shutdown the port build performed
// on SIGINT/SIGTERM and then returns nil so deferred cleanup can run.
func Run(ctx context.Context, opts Options) error {
	wg := new(sync.WaitGroup)

	if opts.MemoryProfiler {
		// Start pprof server for memory profiling on port 6060
		go func() {
			logging.Info("Starting pprof server on :6060", nil)
			if err := http.ListenAndServe(":6060", nil); err != nil {
				logging.Infof("pprof server error: %v", err)
			}
		}()
	}

	settings, err := config.GetConfig()
	if err != nil {
		logging.Fatal("Failed to load configuration", map[string]interface{}{
			"error": err,
		})
	}

	if opts.BootstrapSetup {
		if err := RunBootstrapSetup(ctx, opts.SetupHost, opts.SetupPort, opts.SetupProfile, opts.Stop); err != nil {
			if errors.Is(err, ErrSetupInterrupted) {
				logging.Info("Bootstrap setup interrupted by shutdown request - exiting", nil)
				return nil
			}
			logging.Fatalf("Bootstrap setup failed: %v", err)
		}

		if err := config.InitConfig(); err != nil {
			logging.Fatalf("Failed to reinitialize config after bootstrap setup: %v", err)
		}

		settings, err = config.GetConfig()
		if err != nil {
			logging.Fatalf("Failed to reload config after bootstrap setup: %v", err)
		}
	}

	// Setup may have changed UPnP. Initialize it only from the final reloaded
	// configuration so operator mode never maps ports from template defaults.
	initializeUPnP()

	serializedPrivateKey := viper.GetString("relay.private_key")

	// Add diagnostic logging for config state
	logging.Info("Config initialization diagnostic", map[string]interface{}{
		"config_file_exists":     viper.ConfigFileUsed() != "",
		"config_file_path":       viper.ConfigFileUsed(),
		"private_key_exists":     len(serializedPrivateKey) > 0,
		"legacy_dht_key_exists":  len(viper.GetString("relay.dht_key")) > 0,
		"dht_seed_exists":        len(viper.GetString("relay.dht_seed")) > 0,
		"dht_public_key_exists":  len(viper.GetString("relay.dht_public_key")) > 0,
		"dht_private_key_exists": len(viper.GetString("relay.dht_private_key")) > 0,
		"public_key_exists":      len(viper.GetString("relay.public_key")) > 0,
		"wallet_key_exists":      len(viper.GetString("external_services.wallet.key")) > 0,
	})

	// Track if we need to save config at the end
	configNeedsSave := false

	if len(serializedPrivateKey) <= 0 {
		newKey, err := signing.GeneratePrivateKey()
		if err != nil {
			logging.Fatal("error generating or saving server private key")
		}

		key, err := signing.SerializePrivateKey(newKey)
		if err != nil {
			logging.Fatal("error generating or saving server private key")
		} else {
			viper.Set("relay.private_key", key)
			serializedPrivateKey = *key

			// Also derive and save the public key
			_, publicKey, err := signing.DeserializePrivateKey(serializedPrivateKey)
			if err != nil {
				logging.Fatal("error deriving public key from generated private key")
			}

			serializedPublicKey, err := signing.SerializePublicKey(publicKey)
			if err != nil {
				logging.Fatal("error serializing public key")
			} else {
				viper.Set("relay.public_key", serializedPublicKey)
			}

			// Use UpdateConfig with save=false for all changes during startup
			config.UpdateConfig("relay.private_key", *key, true)
			config.UpdateConfig("relay.public_key", *serializedPublicKey, true)
			configNeedsSave = true

			logging.Info("Generated new server keys", map[string]interface{}{
				"public_key": serializedPublicKey,
			})
		}
	}

	legacyDHTSeed := strings.TrimSpace(viper.GetString("relay.dht_key"))
	dhtSeed := strings.TrimSpace(viper.GetString("relay.dht_seed"))
	if dhtSeed == "" && legacyDHTSeed != "" {
		dhtSeed = legacyDHTSeed
		if err := config.UpdateConfig("relay.dht_seed", dhtSeed, true); err != nil {
			logging.Errorf("Failed to migrate legacy relay.dht_key to relay.dht_seed: %v", err)
		} else {
			configNeedsSave = true
			logging.Info("Migrated legacy relay DHT seed configuration", nil)
		}
	}
	if legacyDHTSeed != "" {
		if err := config.RemoveConfigKey("relay.dht_key"); err != nil {
			logging.Errorf("Failed to remove legacy relay.dht_key config entry: %v", err)
		} else {
			logging.Info("Removed legacy relay.dht_key config entry", nil)
		}
	}

	var dhtIdentity *synckeys.DHTIdentity
	if dhtSeed == "" {
		dhtIdentity, err = synckeys.DeriveDHTIdentityFromPrivateKey(serializedPrivateKey)
		if err != nil {
			logging.Errorf("Failed to derive DHT identity from relay private key: %v", err)
		} else {
			dhtSeed = dhtIdentity.Seed
			if err := config.UpdateConfig("relay.dht_seed", dhtSeed, true); err != nil {
				logging.Errorf("Failed to save derived relay DHT seed: %v", err)
			} else {
				configNeedsSave = true
				logging.Info("Generated new relay DHT seed from private key", nil)
			}
		}
	} else {
		dhtIdentity, err = synckeys.DeriveDHTIdentityFromSeed(dhtSeed)
		if err != nil {
			logging.Errorf("Failed to derive DHT identity from configured seed: %v", err)
		}
	}

	if dhtIdentity != nil {
		if currentDHTPublicKey := strings.TrimSpace(viper.GetString("relay.dht_public_key")); currentDHTPublicKey != dhtIdentity.PublicKey {
			if err := config.UpdateConfig("relay.dht_public_key", dhtIdentity.PublicKey, true); err != nil {
				logging.Errorf("Failed to save relay DHT public key: %v", err)
			} else {
				configNeedsSave = true
				logging.Info("Updated relay DHT public key in configuration", map[string]interface{}{
					"dht_public_key": dhtIdentity.PublicKey,
				})
			}
		}

		if currentDHTPrivateKey := strings.TrimSpace(viper.GetString("relay.dht_private_key")); currentDHTPrivateKey != dhtIdentity.PrivateKey {
			if err := config.UpdateConfig("relay.dht_private_key", dhtIdentity.PrivateKey, true); err != nil {
				logging.Errorf("Failed to save relay DHT private key: %v", err)
			} else {
				configNeedsSave = true
				logging.Info("Updated relay DHT private key in configuration", nil)
			}
		}
	}

	if viper.GetInt("server.services.airlock.port") > 0 {
		airlockDHTPublicKey, err := syncAirlockServiceDHTPubkey()
		if err != nil {
			logging.Errorf("Failed to synchronize Airlock DHT public key for service discovery: %v", err)
		} else if currentAirlockDHTPublicKey := strings.TrimSpace(viper.GetString("server.services.airlock.dht_pubkey")); currentAirlockDHTPublicKey != airlockDHTPublicKey {
			if err := config.UpdateConfig("server.services.airlock.dht_pubkey", airlockDHTPublicKey, true); err != nil {
				logging.Errorf("Failed to save Airlock DHT public key for service discovery: %v", err)
			} else {
				configNeedsSave = true
				logging.Info("Synchronized Airlock DHT public key for service discovery", map[string]interface{}{
					"airlock_dht_public_key": airlockDHTPublicKey,
				})
			}
		}
	}

	// Generate wallet API key if not set
	walletAPIKey := viper.GetString("external_services.wallet.key")

	if len(walletAPIKey) <= 0 {
		newAPIKey, err := config.GenerateRandomAPIKey()
		if err != nil {
			logging.Errorf("Failed to generate wallet API key: %v", err)
		} else {
			// Use UpdateConfig with save=false
			config.UpdateConfig("external_services.wallet.key", newAPIKey, true)
			configNeedsSave = true

			logging.Info("Generated new wallet API key", nil)
		}
	}

	privateKey, publicKey, err := signing.DeserializePrivateKey(serializedPrivateKey)
	if err != nil {
		logging.Fatal("failed to deserialize private key")
	}

	// Ensure public key matches the private key, but only save if it differs
	serializedPublicKey, err := signing.SerializePublicKey(publicKey)
	if err != nil {
		logging.Errorf("Failed to serialize public key: %v", err)
	} else {
		currentPublicKey := viper.GetString("relay.public_key")

		// Only update if the public key has actually changed
		if currentPublicKey != *serializedPublicKey {
			// Use UpdateConfig with save=false
			config.UpdateConfig("relay.public_key", *serializedPublicKey, true)
			configNeedsSave = true

			logging.Info("Updated public key in configuration (derived from private key)", map[string]interface{}{
				"public_key": *serializedPublicKey,
			})
		} else {
			logging.Info("Public key already matches derived key, no config update needed", map[string]interface{}{
				"public_key": *serializedPublicKey,
			})
		}
	}

	// Save config ONCE if any changes were made during startup
	// Use UpdateConfig with a dummy key to trigger the save
	if configNeedsSave {
		logging.Info("Saving startup configuration changes...")
		// Force a save by updating a timestamp
		err = config.UpdateConfig("startup_initialized", time.Now().Unix(), true)
		if err != nil {
			logging.Fatal("Failed to save startup configuration", map[string]interface{}{
				"error": err,
			})
		}
		logging.Info("Startup configuration saved successfully")
	}

	// Create HyperDHT listener via sidecar
	hsClient, err := sidecar.GetClient()
	if err != nil {
		logging.Fatal("Failed to connect to hyperswarm sidecar", map[string]interface{}{
			"error": err,
		})
	}
	defer sidecar.Close()
	sidecarSup := newSidecarSupervisor(hsClient, forwardSidecarDHTPort(hsClient))
	defer sidecarSup.CleanupUPnP()

	listener := hsListener.NewHyperswarmListener(hsClient)
	defer listener.Close()

	_, dhtPublicKey, err := listener.CreateServerFromSeed(dhtSeed)
	if err != nil {
		logging.Fatal("Failed to create HyperDHT server", map[string]interface{}{
			"error": err,
		})
	}

	// Store DHT public key for NIP-11 advertisement
	viper.Set("DHTPublicKey", dhtPublicKey)
	if currentDHTPublicKey := strings.TrimSpace(viper.GetString("relay.dht_public_key")); currentDHTPublicKey != dhtPublicKey {
		if err := config.UpdateConfig("relay.dht_public_key", dhtPublicKey, true); err != nil {
			logging.Errorf("Failed to save sidecar DHT public key: %v", err)
		} else {
			configNeedsSave = true
			logging.Info("Synchronized relay DHT public key with sidecar server", map[string]interface{}{
				"dht_public_key": dhtPublicKey,
			})
		}
	}
	if dhtIdentity != nil && dhtIdentity.PublicKey != dhtPublicKey {
		logging.Errorf("Derived DHT public key does not match sidecar server public key: derived=%s sidecar=%s", dhtIdentity.PublicKey, dhtPublicKey)
	}

	// Create and initialize database
	store, err := badgerhold.InitStore(config.GetPath("store"))
	if err != nil {
		logging.Fatal(err.Error())
	}

	// Handle --compact flag: run compaction BEFORE setting up any network services
	// This ensures no incoming connections during compaction
	if opts.CompactDB {
		logging.Info("Running compaction before starting relay services...", nil)
		lsm, vlog := store.GetDatabaseSize()
		logging.Info("Current database size", map[string]interface{}{
			"lsm_mb":   lsm / (1024 * 1024),
			"vlog_mb":  vlog / (1024 * 1024),
			"total_mb": (lsm + vlog) / (1024 * 1024),
		})

		workers := runtime.NumCPU()
		logging.Infof("Starting compaction with %d workers. This may take a while...", workers)

		startTime := time.Now()
		if err := store.Compact(workers); err != nil {
			logging.Fatal("Compaction failed", map[string]interface{}{
				"error": err,
			})
		}

		lsmAfter, vlogAfter := store.GetDatabaseSize()
		logging.Info("Compaction complete", map[string]interface{}{
			"duration_seconds": time.Since(startTime).Seconds(),
			"lsm_mb_before":    lsm / (1024 * 1024),
			"lsm_mb_after":     lsmAfter / (1024 * 1024),
			"vlog_mb_before":   vlog / (1024 * 1024),
			"vlog_mb_after":    vlogAfter / (1024 * 1024),
			"space_saved_mb":   (vlog - vlogAfter) / (1024 * 1024),
		})
	}

	// Initialize image moderation system if enabled
	if config.IsEnabled("content_filtering.image_moderation.enabled") {
		defer func() {
			// Shutdown moderation system if initialized
			moderation.Shutdown()
		}()

		logging.Info("Initializing image moderation system...")

		// Get moderation configuration from config
		apiEndpoint := config.GetExternalURL("moderator")
		threshold := viper.GetFloat64("content_filtering.image_moderation.threshold")
		mode := viper.GetString("content_filtering.image_moderation.mode")
		timeout := time.Duration(viper.GetInt("content_filtering.image_moderation.timeout_seconds")) * time.Second
		checkInterval := time.Duration(viper.GetInt("content_filtering.image_moderation.check_interval_seconds")) * time.Second
		tempDir := config.GetPath("moderation")
		concurrency := viper.GetInt("content_filtering.image_moderation.concurrency")

		// Make sure temp directory exists
		if _, err := os.Stat(tempDir); os.IsNotExist(err) {
			if err := os.MkdirAll(tempDir, 0755); err != nil {
				logging.Infof("Failed to create moderation temp directory: %v", err)
				tempDir = os.TempDir() // Fallback to system temp dir
			}
		}

		// Initialize moderation system
		err := moderation.InitModeration(
			store,
			apiEndpoint,
			moderation.WithThreshold(threshold),
			moderation.WithMode(mode),
			moderation.WithTimeout(timeout),
			moderation.WithCheckInterval(checkInterval),
			moderation.WithTempDir(tempDir),
			moderation.WithConcurrency(concurrency),
		)

		if err != nil {
			logging.Errorf("Failed to initialize image moderation: %v", err)
		} else {
			logging.Info("Image moderation system initialized successfully")
		}
	} else {
		logging.Info("Image moderation system is disabled")
	}

	// Initialize subscription manager with tiers from allowed_users
	subscription.InitGlobalManager(
		store,
		privateKey,
		dhtPublicKey,
		settings.AllowedUsersSettings.Tiers,
	)
	logging.Info("Global subscription manager initialized successfully")

	// Create a cancellable context for background operations
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// WaitGroup to track background goroutines
	var bgWg sync.WaitGroup

	// Batch update existing kind 11888 events on startup if enabled
	if viper.GetBool("allowed_users.batch_update_on_startup") {
		logging.Info("Batch update is enabled - updating existing kind 11888 events to reflect current configuration...")
		if manager := subscription.GetGlobalManager(); manager != nil {
			bgWg.Add(1)
			go func() {
				defer bgWg.Done()
				if err := manager.BatchUpdateAllSubscriptionEventsWithContext(ctx); err != nil {
					if ctx.Err() == nil { // Only log error if not cancelled
						logging.Errorf("Failed to update existing kind 11888 events on startup: %v", err)
					}
				} else {
					logging.Info("Successfully updated existing kind 11888 events on startup")
				}
			}()
		}
	} else {
		logging.Info("Batch update on startup is disabled (set allowed_users.batch_update_on_startup to true to enable)")
	}

	// Initialize daily free tier subscription renewal
	logging.Info("Initializing daily free tier subscription renewal...")
	subscription.InitDailyFreeSubscriptionRenewal()

	// Validate subscription mode configuration and ensure address availability
	if settings.AllowedUsersSettings.Mode == "subscription" {
		logging.Info("Relay is in subscription mode - validating Bitcoin address availability...")
		if manager := subscription.GetGlobalManager(); manager != nil {
			if err := validateSubscriptionModeStartup(manager, store); err != nil {
				logging.Fatal("Subscription mode validation failed", map[string]interface{}{
					"error": err,
				})
			}
		} else {
			logging.Fatal("Subscription manager not available for subscription mode validation")
		}
	} else {
		logging.Info("Relay not in subscription mode - skipping Bitcoin address validation", map[string]interface{}{
			"mode": settings.AllowedUsersSettings.Mode,
		})
	}

	// Initialize the global access control
	logging.Info("Initializing global access control...")
	if statsStore := store.GetStatsStore(); statsStore != nil {
		if err := ws.InitializeAccessControl(statsStore); err != nil {
			logging.Errorf("Failed to initialize access control: %v", err)
		} else {
			logging.Info("Global access control initialized successfully")

			// Populate blacklist cache from stored kind 30078 events at startup
			if ac := websocket.GetAccessControl(); ac != nil && ac.Blacklist != nil {
				ac.Blacklist.PopulateFromStore(store)
				logging.Info("Blacklist cache populated from stored events")
			}

			// Set the WOT cache lazy-loader so WOT graphs are automatically
			// reloaded from the DAG store on cache miss (after restart or LRU eviction).
			if ac := websocket.GetAccessControl(); ac != nil && ac.WotCache != nil {
				ac.WotCache.SetLoader(func(dagRootHash string) ([]byte, string, error) {
					dagData, err := store.BuildDagFromStore(dagRootHash, true)
					if err != nil {
						return nil, "", fmt.Errorf("DAG not found in store: %w", err)
					}
					rootLeaf := dagData.Dag.Leafs[dagData.Dag.Root]
					if rootLeaf == nil {
						return nil, "", fmt.Errorf("root leaf missing from DAG")
					}
					ownerPubkey := rootLeaf.AdditionalData["wot_owner"]
					if ownerPubkey == "" {
						return nil, "", fmt.Errorf("DAG has no wot_owner tag")
					}
					binaryData, err := dagData.Dag.GetContentFromLeaf(rootLeaf)
					if err != nil {
						return nil, "", fmt.Errorf("failed to extract content: %w", err)
					}
					return binaryData, ownerPubkey, nil
				})
				logging.Info("WOT cache lazy-loader configured (auto-reload from store on cache miss)")
			}
		}

		// Initialize push notification service
		logging.Info("Initializing push notification service...")
		if err := push.InitGlobalPushService(store); err != nil {
			logging.Errorf("Failed to initialize push notification service: %v", err)
		} else {
			logging.Info("Push notification service initialized successfully")
		}
	} else {
		logging.Warn("Warning: Statistics store not available, access control and push notifications not initialized")
	}

	// Create and store kind 10411 event
	if err := kind10411.CreateKind10411Event(privateKey, publicKey, store); err != nil {
		logging.Errorf("Failed to create kind 10411 event: %v", err)
		// The original entrypoint returned from main() here - a soft stop
		// with exit code 0 after logging - so the shared core preserves
		// that by returning nil.
		return nil
	}

	// Stream Handlers
	download.AddDownloadHandler(listener, store, func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		accessControl := websocket.GetAccessControl()
		if accessControl == nil {
			return true
		}

		requesterPubkey := ""
		requesterSignature := ""
		if pubKey != nil {
			requesterPubkey = *pubKey
		}
		if signature != nil {
			requesterSignature = *signature
		}

		return accessControl.CanReadDag(rootLeaf, requesterPubkey, requesterSignature, store) == nil
	})

	upload.AddUploadHandler(listener, store, func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		accessControl := websocket.GetAccessControl()
		if accessControl == nil {
			return true
		}

		requesterPubkey := ""
		if pubKey != nil {
			requesterPubkey = *pubKey
		}

		return accessControl.CanWriteDag(rootLeaf, requesterPubkey, store) == nil
	}, func(dag *merkle_dag.Dag, pubKey *string) {
		// Detect WOT DAGs by checking root leaf's AdditionalData for wot_file tag.
		// If present, load the binary content and cache it for WOT permission checks.
		rootLeaf := dag.Leafs[dag.Root]
		if rootLeaf == nil || rootLeaf.AdditionalData == nil {
			return
		}
		if rootLeaf.AdditionalData["wot_file"] != "true" {
			return
		}

		ownerPubkey := rootLeaf.AdditionalData["wot_owner"]
		if ownerPubkey == "" && pubKey != nil {
			ownerPubkey = *pubKey
		}

		logging.Infof("WOT DAG detected (root: %s, owner: %s) — loading binary for cache", dag.Root, ownerPubkey)

		// Re-read the DAG with content to get the binary bytes
		dagData, err := store.BuildDagFromStore(dag.Root, true)
		if err != nil {
			logging.Errorf("Failed to load WOT DAG content for caching: %v", err)
			return
		}

		// For a single-file WOT upload, the content is in the root leaf or reassembled
		contentLeaf := dagData.Dag.Leafs[dagData.Dag.Root]
		if contentLeaf == nil {
			return
		}
		binaryData, err := dagData.Dag.GetContentFromLeaf(contentLeaf)
		if err != nil || len(binaryData) == 0 {
			logging.Errorf("Failed to extract WOT binary from DAG: %v", err)
			return
		}

		// Try to cache it — if parsing fails, the cache.Store call returns an error
		// and the DAG is treated as a normal upload (fail-safe).
		accessControl := websocket.GetAccessControl()
		if accessControl == nil || accessControl.WotCache == nil {
			logging.Infof("WOT cache not available, skipping cache for DAG %s", dag.Root)
			return
		}

		if err := accessControl.WotCache.Store(dag.Root, ownerPubkey, binaryData); err != nil {
			logging.Infof("WOT DAG %s did not parse as valid WOT binary (proceeding as normal DAG): %v", dag.Root, err)
		} else {
			logging.Infof("WOT DAG %s cached successfully for owner %s", dag.Root, ownerPubkey)
		}
	})
	query.AddQueryHandler(listener, store)
	claim.AddClaimOwnershipHandler(listener, store)
	services.AddServicesHandler(listener)
	nostr_relay.AddNostrRelayHandler(listener, store)

	logging.Infof("HyperDHT server started with public key: %s\n", dhtPublicKey)

	// Register All Nostr Stream Handlers
	// Always register all specific handlers for registered kinds
	logging.Info("Registering all specific kind handlers...")
	nostr.RegisterHandler("kind/0", kind0.BuildKind0Handler(store, privateKey))
	nostr.RegisterHandler("kind/1", kind1.BuildKind1Handler(store))
	nostr.RegisterHandler("kind/1808", kind1808.BuildKind1808Handler(store))
	nostr.RegisterHandler("kind/1809", kind1809.BuildKind1809Handler(store))
	nostr.RegisterHandler("kind/3", kind3.BuildKind3Handler(store))
	nostr.RegisterHandler("kind/443", kind443.BuildKind443Handler(store))
	nostr.RegisterHandler("kind/444", kind444.BuildKind444Handler(store))
	nostr.RegisterHandler("kind/445", kind445.BuildKind445Handler(store))
	nostr.RegisterHandler("kind/1059", kind1059.BuildKind1059Handler(store))
	nostr.RegisterHandler("kind/5", kind5.BuildKind5Handler(store))
	nostr.RegisterHandler("kind/72", kind72.BuildKind72Handler(store))
	nostr.RegisterHandler("kind/77", kind77.BuildKind77Handler(store))
	nostr.RegisterHandler("kind/6", kind6.BuildKind6Handler(store))
	nostr.RegisterHandler("kind/7", kind7.BuildKind7Handler(store))
	nostr.RegisterHandler("kind/8", kind8.BuildKind8Handler(store))
	nostr.RegisterHandler("kind/1984", kind1984.BuildKind1984Handler(store))
	nostr.RegisterHandler("kind/9735", kind9735.BuildKind9735Handler(store))
	nostr.RegisterHandler("kind/9372", kind9372.BuildKind9372Handler(store))
	nostr.RegisterHandler("kind/9373", kind9373.BuildKind9373Handler(store))
	nostr.RegisterHandler("kind/9802", kind9802.BuildKind9802Handler(store))
	nostr.RegisterHandler("kind/10000", kind10000.BuildKind10000Handler(store))
	nostr.RegisterHandler("kind/10001", kind10001.BuildKind10001Handler(store))
	nostr.RegisterHandler("kind/10002", kind10002.BuildKind10002Handler(store))
	nostr.RegisterHandler("kind/10051", kind10051.BuildKind10051Handler(store))
	nostr.RegisterHandler("kind/11011", kind11011.BuildKind11011Handler(store))
	nostr.RegisterHandler("kind/22242", auth.BuildAuthHandler(store))
	nostr.RegisterHandler("kind/30000", kind30000.BuildKind30000Handler(store))
	nostr.RegisterHandler("kind/30008", kind30008.BuildKind30008Handler(store))
	nostr.RegisterHandler("kind/30009", kind30009.BuildKind30009Handler(store))
	nostr.RegisterHandler("kind/30023", kind30023.BuildKind30023Handler(store))
	nostr.RegisterHandler("kind/30078", kind30078.BuildKind30078Handler(store))
	nostr.RegisterHandler("kind/30079", kind30079.BuildKind30079Handler(store))
	// Pass WotCache to kind 31415 handler so it can invalidate stale WOT cache entries
	// when permission events are updated with new wot_file tags.
	var wotCacheForHandler *wot.Cache
	if ac := websocket.GetAccessControl(); ac != nil {
		wotCacheForHandler = ac.WotCache
	}
	nostr.RegisterHandler("kind/31415", kind16629.BuildKind31415Handler(store, wotCacheForHandler))
	nostr.RegisterHandler("kind/16630", kind16630.BuildKind16630Handler(store))
	nostr.RegisterHandler("kind/10010", kind10010.BuildKind10010Handler(store))
	nostr.RegisterHandler("kind/19841", kind19841.BuildKind19841Handler(store))
	nostr.RegisterHandler("kind/19842", kind19842.BuildKind19842Handler(store))
	nostr.RegisterHandler("kind/19843", kind19843.BuildKind19843Handler(store))
	nostr.RegisterHandler("kind/1063", kind1063.BuildKind1063Handler(store))

	// Always register universal handler for unregistered kinds
	nostr.RegisterHandler("universal", universal.BuildUniversalHandler(store))

	if viper.GetBool("event_filtering.allow_unregistered_kinds") {
		logging.Info("Universal handler registered for unregistered kinds (allow_unregistered_kinds=true)")
	} else {
		logging.Info("Universal handler registered but unregistered kinds are disabled (allow_unregistered_kinds=false)")
	}

	nostr.RegisterHandler("filter", filter.BuildFilterHandler(store))
	nostr.RegisterHandler("count", count.BuildCountsHandler(store))

	// Auth event not supported for the stream connections yet
	//nostr.RegisterHandler("auth", auth.BuildAuthHandler(store))

	// Register a hyperswarm handler for every nostr stream handler
	for kind := range nostr.GetHandlers() {
		handler := nostr.GetHandler(kind)

		wrapper := func(stream lib_types.Stream) {
			defer stream.Close()

			read := func() ([]byte, error) {
				decoder := json.NewDecoder(stream)

				var rawMessage json.RawMessage
				err := decoder.Decode(&rawMessage)
				if err != nil {
					return nil, err
				}

				return rawMessage, nil
			}

			write := func(messageType string, params ...interface{}) {
				response := nostr.BuildResponse(messageType, params)

				if len(response) > 0 {
					stream.Write(response)
				}
			}

			handler(read, write)
		}

		listener.SetStreamHandler("/nostr/event/"+kind, wrapper)
	}

	// Supervise the sidecar connection: if the sidecar process dies or the
	// connection is lost, re-acquire a client and re-declare the DHT server
	// and every registered protocol so the relay's P2P presence recovers
	// automatically instead of staying silently offline. Started after all
	// protocol registration so a recovery re-declares every handler.
	sidecarSup.Start(ctx, listener, dhtSeed)

	// Web Panel
	if config.IsEnabled("web") {
		wg.Add(1)

		logging.Info("Starting with web server enabled")

		go func() {
			err = web.StartServer(store, ctx)

			if err != nil {
				logging.Info("Fatal error occurred in web server")
			}

			wg.Done()
		}()
	}

	// Nostr web sockets
	var wsApp *fiber.App
	if config.IsEnabled("nostr") {
		wg.Add(1)

		logging.Info("Starting with legacy nostr proxy web server enabled")

		go func() {
			wsApp = ws.BuildServer(store)
			err := ws.StartServer(wsApp)

			if err != nil {
				logging.Info("Fatal error occurred in nostr server")
			}

			wg.Done()
		}()
	}

	// Blossom file storage
	var blossomApp *fiber.App
	if config.IsEnabled("blossom") {
		wg.Add(1)

		logging.Info("Starting Blossom file storage server")

		go func() {
			blossomApp = ws.BuildBlossomServer(store)
			err := ws.StartBlossomServer(blossomApp)

			if err != nil {
				logging.Info("Fatal error occurred in blossom server")
			}

			wg.Done()
		}()
	}

	// Handle graceful shutdown requests from the caller. The port entrypoint
	// wires opts.Stop to SIGINT/SIGTERM; the Windows service entrypoint wires
	// it to SCM stop requests. The cleanup sequence is identical to the
	// original signal handler.
	shutdownDone := make(chan struct{})

	go func() {
		if opts.Stop == nil {
			return
		}
		<-opts.Stop
		logging.Info("Received shutdown signal, cleaning up...")

		// 1. Signal WebSocket connections to stop accepting new operations
		ws.SignalShutdown()

		if wsApp != nil {
			logging.Info("Shutting down WebSocket server...")
			if err := wsApp.Shutdown(); err != nil {
				logging.Errorf("Error shutting down WebSocket server: %v", err)
			}
		}

		if blossomApp != nil {
			logging.Info("Shutting down Blossom server...")
			if err := blossomApp.Shutdown(); err != nil {
				logging.Errorf("Error shutting down Blossom server: %v", err)
			}
		}

		// 2. Wait for active WebSocket connections to close gracefully
		logging.Info("Waiting for active WebSocket connections to close...")
		if ws.WaitForConnections(10 * time.Second) {
			logging.Info("All WebSocket connections closed gracefully")
		} else {
			logging.Info("Timeout waiting for WebSocket connections, proceeding with cleanup")
		}

		cancel()

		done := make(chan struct{})
		go func() {
			bgWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			logging.Info("All background operations completed")
		case <-time.After(5 * time.Second):
			logging.Info("Timeout waiting for background operations, proceeding with cleanup")
		}

		push.StopGlobalPushService()

		logging.Info("Closing database...")
		if err := store.Cleanup(); err != nil {
			logging.Errorf("Error during database cleanup: %v", err)
		}

		logging.Info("Shutdown complete")
		close(shutdownDone)
	}()

	// The original entrypoint blocked on wg.Wait() and ended its shutdown
	// handler with os.Exit(0), which skipped every deferred cleanup (sidecar
	// Close, listener Close, UPnP unmapping). Returning here instead
	// reproduces both exits - natural server exit and completed shutdown -
	// while letting the deferred cleanup actually run.
	serversDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(serversDone)
	}()

	select {
	case <-serversDone:
	case <-shutdownDone:
	}

	return nil
}

// validateSubscriptionModeStartup ensures that when the relay starts in subscription mode,
// all existing Kind 11888 events have Bitcoin addresses assigned and the wallet service is available
func validateSubscriptionModeStartup(manager *subscription.SubscriptionManager, store *badgerhold.BadgerholdStore) error {
	logging.Info("Starting subscription mode validation at startup...")

	// Step 1: Check wallet service connectivity using subscription package function
	walletHealthy, err := subscription.CheckWalletServiceHealth()
	if err != nil || !walletHealthy {
		return fmt.Errorf("wallet service is not available - cannot start in subscription mode: %v", err)
	}
	logging.Info("Wallet service connectivity verified")

	// Step 2: Check if there are existing Kind 11888 events without Bitcoin addresses
	statsStore := store.GetStatsStore()
	if statsStore == nil {
		return fmt.Errorf("statistics store not available")
	}

	usersWithoutAddresses, err := statsStore.CountUsersWithoutBitcoinAddresses()
	if err != nil {
		return fmt.Errorf("failed to count users without Bitcoin addresses: %v", err)
	}

	if usersWithoutAddresses == 0 {
		logging.Info("All existing users already have Bitcoin addresses assigned")
		return nil
	}

	logging.Info("Found users without Bitcoin addresses, starting migration", map[string]interface{}{
		"users_needing_addresses": usersWithoutAddresses,
	})

	// Step 3: Run the batch allocation to assign addresses to existing users
	// This will handle ensuring sufficient addresses are available
	if err := manager.AllocateBitcoinAddressesForExistingUsers(); err != nil {
		return fmt.Errorf("failed to allocate Bitcoin addresses for existing users: %v", err)
	}

	logging.Info("Successfully completed subscription mode startup validation", map[string]interface{}{
		"users_processed": usersWithoutAddresses,
	})

	return nil
}
