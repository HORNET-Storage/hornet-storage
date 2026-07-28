package websocket

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/spf13/viper"

	"github.com/HORNET-Storage/hornet-storage/lib/config"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/blossom"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/upnp"
	"github.com/HORNET-Storage/hornet-storage/lib/web/handlers/push"
	"github.com/HORNET-Storage/hornet-storage/lib/web/middleware"
)

// Graceful shutdown coordination
var (
	shutdownChan      = make(chan struct{})
	shutdownOnce      sync.Once
	activeConnWg      sync.WaitGroup
	shutdownInitiated bool
	shutdownMu        sync.RWMutex
)

// SignalShutdown signals all WebSocket connections to stop processing
func SignalShutdown() {
	shutdownOnce.Do(func() {
		shutdownMu.Lock()
		shutdownInitiated = true
		shutdownMu.Unlock()
		close(shutdownChan)
		logging.Info("WebSocket shutdown signal sent")
	})
}

// IsShuttingDown returns true if shutdown has been initiated
func IsShuttingDown() bool {
	shutdownMu.RLock()
	defer shutdownMu.RUnlock()
	return shutdownInitiated
}

// WaitForConnections waits for all active WebSocket connections to close
// Returns true if all connections closed, false if timeout was reached
func WaitForConnections(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		activeConnWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

type connectionState struct {
	authenticated   bool
	pubkey          string    // Store the pubkey to know who owns this connection
	blockedCheck    time.Time // When we last checked if this pubkey is blocked
	authAttempts    int       // Track number of auth attempts
	lastAuthAttempt time.Time // Track last auth attempt time
}

// isConnectionBlocked checks if a connection's pubkey is blocked
func isConnectionBlocked(state *connectionState, store stores.Store) (bool, error) {
	// Skip if no pubkey (not authenticated yet)
	if state.pubkey == "" {
		return false, nil
	}

	// Only check once per minute to avoid excessive database lookups
	if time.Since(state.blockedCheck) < time.Minute {
		return false, nil
	}

	// Check if pubkey is blocked
	state.blockedCheck = time.Now()
	return store.IsBlockedPubkey(state.pubkey)
}

// terminateIfBlocked checks if a connection is blocked and terminates it if so
func terminateIfBlocked(c *websocket.Conn, state *connectionState, store stores.Store) bool {
	isBlocked, err := isConnectionBlocked(state, store)
	if err != nil {
		logging.Infof("Error checking if pubkey is blocked: %v", err)
		return false
	}

	if isBlocked {
		logging.Infof("Terminating connection from blocked pubkey: %s", state.pubkey)
		c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4403, "Blocked pubkey"))
		c.Close()
		return true
	}
	return false
}

func BuildServer(store stores.Store) *fiber.App {
	// Start the async notification processor before accepting connections.
	// Events are queued via notifyListeners() and fan-out happens on a
	// dedicated goroutine, so websocket handlers are never blocked.
	StartNotificationProcessor(store)

	app := fiber.New()

	// Middleware for handling relay information requests
	app.Use(handleRelayInfoRequests)

	// ================================
	// PUSH NOTIFICATION ROUTES (NIP-98 AUTH)
	// ================================

	pushRoutes := app.Group("/push")

	// Add basic request logging for push routes
	pushRoutes.Use(func(c *fiber.Ctx) error {
		logging.Info("Push route accessed", map[string]interface{}{
			"method": c.Method(),
			"path":   c.Path(),
			"url":    c.OriginalURL(),
		})
		return c.Next()
	})

	// Apply NIP-98 authentication middleware for push routes
	pushRoutes.Use(middleware.NIP98Middleware())

	pushRoutes.Post("/register", push.RegisterDeviceHandler(store))
	pushRoutes.Post("/unregister", push.UnregisterDeviceHandler(store))
	pushRoutes.Post("/test", push.TestNotificationHandler(store))

	app.Get("/", websocket.New(func(c *websocket.Conn) {
		// Track this connection for graceful shutdown
		activeConnWg.Add(1)
		defer activeConnWg.Done()
		defer removeListener(c)

		// Check if shutdown is already in progress
		if IsShuttingDown() {
			c.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(1001, "Server is shutting down"))
			return
		}

		challenge := getGlobalChallenge()

		// Initialize state with empty pubkey and current time for blocked check
		state := &connectionState{
			authenticated: false,
			pubkey:        "",
			blockedCheck:  time.Now(),
		}

		// Send the AUTH challenge immediately upon connection
		authChallenge := []interface{}{"AUTH", challenge}
		jsonAuth, err := json.Marshal(authChallenge)
		if err != nil {
			logging.Infof("Error marshalling auth interface: %v", err)
		}

		handleIncomingMessage(c, jsonAuth)

		// Start a background goroutine to periodically check if the pubkey becomes blocked
		connClosed := make(chan struct{})
		defer close(connClosed)

		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()

			for {
				select {
				case <-shutdownChan:
					// Server is shutting down, stop background checks
					return
				case <-ticker.C:
					// Skip database operations if shutting down or store is closed
					if IsShuttingDown() || store.IsClosed() {
						return
					}
					if state.pubkey != "" {
						isBlocked, err := store.IsBlockedPubkey(state.pubkey)
						if err != nil {
							logging.Infof("Error checking if pubkey is blocked: %v", err)
							continue
						}

						if isBlocked {
							logging.Infof("Terminating connection from newly blocked pubkey: %s", state.pubkey)
							c.WriteMessage(websocket.CloseMessage,
								websocket.FormatCloseMessage(4403, "Blocked pubkey"))
							c.Close()
							return
						}
					}
				case <-connClosed:
					return
				}
			}
		}()

		for {
			// Check for shutdown before processing each message
			select {
			case <-shutdownChan:
				logging.Info("Connection closing due to server shutdown")
				c.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(1001, "Server is shutting down"))
				return
			default:
				// Continue processing
			}

			if err := processWebSocketMessage(c, challenge, state, store); err != nil {
				break
			}
		}
	}))

	return app
}

// BuildBlossomServer creates a separate Fiber app for Blossom file storage
func BuildBlossomServer(store stores.Store) *fiber.App {
	app := fiber.New()

	// Enable blossom routes for unchunked file storage
	server := blossom.NewServer(store)
	server.SetupRoutes(app)

	return app
}

// StartBlossomServer starts the Blossom server on its own port (base_port + 5)
func StartBlossomServer(app *fiber.App) error {
	address := viper.GetString("server.bind_address")
	port := config.GetPort("blossom")
	cleanupUPnP := forwardPortIfEnabled(port, "Hornet Storage Blossom")
	defer cleanupUPnP()

	logging.Info("Starting Blossom server", map[string]interface{}{
		"address": address,
		"port":    port,
	})

	err := app.Listen(fmt.Sprintf("%s:%d", address, port))
	if err != nil {
		logging.Fatalf("error starting blossom server: %v\n", err)
	}

	return err
}

func StartServer(app *fiber.App) error {
	// Generate the global challenge
	_, err := generateGlobalChallenge()
	if err != nil {
		logging.Fatalf("Failed to generate global challenge: %v", err)
	}

	address := viper.GetString("server.bind_address")
	port := config.GetPort("nostr")
	cleanupUPnP := forwardPortIfEnabled(port, "Hornet Storage Nostr Relay")
	defer cleanupUPnP()

	err = app.Listen(fmt.Sprintf("%s:%d", address, port))
	if err != nil {
		logging.Fatalf("error starting nostr server: %v\n", err)
	}

	return err
}

func forwardPortIfEnabled(port int, description string) func() {
	if !viper.GetBool("server.upnp") {
		return func() {}
	}

	upnpManager := upnp.Get()
	if upnpManager == nil {
		logging.Warn("UPnP is enabled but no router was discovered", map[string]interface{}{
			"port": port,
		})
		return func() {}
	}

	if err := upnpManager.ForwardPort(uint16(port), description); err != nil {
		logging.Error("Failed to forward port using UPnP", map[string]interface{}{
			"port":  port,
			"error": err,
		})
		return func() {}
	}

	logging.Info("Forwarded port using UPnP", map[string]interface{}{
		"port":        port,
		"description": description,
	})

	return func() {
		upnpManager.RemovePort(uint16(port))
	}
}

func handleRelayInfoRequests(c *fiber.Ctx) error {
	if c.Method() == "GET" && c.Get("Accept") == "application/nostr+json" {
		relayInfo := GetRelayInfo()
		c.Set("Access-Control-Allow-Origin", "*")
		return c.JSON(relayInfo)
	}
	return c.Next()
}

func advertisedNIPs() []int {
	configured := viper.GetIntSlice("relay.supported_nips")
	notificationStoreMu.RLock()
	store := notificationStore
	notificationStoreMu.RUnlock()
	if store != nil && store.SearchReady() {
		return configured
	}
	advertised := make([]int, 0, len(configured))
	for _, nip := range configured {
		if nip != 50 {
			advertised = append(advertised, nip)
		}
	}
	return advertised
}

func GetRelayInfo() NIP11RelayInfo {
	// Format contact as "email | npub"
	var contact string
	email := viper.GetString("relay.contact")
	publicKeyHex := viper.GetString("relay.public_key")

	if email != "" && publicKeyHex != "" {
		// Convert hex public key to npub format
		if npub, err := nip19.EncodePublicKey(publicKeyHex); err == nil {
			contact = fmt.Sprintf("%s | %s", email, npub)
		} else {
			contact = email // fallback to just email if conversion fails
		}
	} else if email != "" {
		contact = email
	} else if publicKeyHex != "" {
		// Convert hex public key to npub format
		if npub, err := nip19.EncodePublicKey(publicKeyHex); err == nil {
			contact = npub
		}
	}

	// These values are set in main.go init() for backward compatibility
	basePort := viper.GetInt("server.port")
	relayMode := viper.GetString("allowed_users.mode")
	readAccess := viper.GetString("allowed_users.read")
	writeAccess := viper.GetString("allowed_users.write")
	relayInfo := NIP11RelayInfo{
		Name:          viper.GetString("relay.name"),
		Description:   viper.GetString("relay.description"),
		Pubkey:        publicKeyHex, // Keep for internal use, excluded from JSON
		RelayMode:     relayMode,
		ReadAccess:    readAccess,
		WriteAccess:   writeAccess,
		Contact:       contact,
		Icon:          viper.GetString("relay.icon"),
		SupportedNIPs: advertisedNIPs(),
		Software:      viper.GetString("relay.software"),
		Version:       viper.GetString("relay.version"),
		BasePort:      basePort, // Clients use this + offsets to find services
	}

	// Build services map for external services only (services not derivable from offset)
	relayInfo.Services = buildServicesMap()

	privKey, _, err := signing.DeserializePrivateKey(viper.GetString("relay.private_key"))
	dhtPubkey := viper.GetString("DHTPublicKey")
	if dhtPubkey != "" && err == nil {
		relayInfo.DHTPubkey = dhtPubkey
		relayInfo.HornetExtension = &HornetExtension{
			RelayMode:   relayMode,
			ReadAccess:  readAccess,
			WriteAccess: writeAccess,
			DHTPubkey:   dhtPubkey,
		}
		err = SignRelay(&relayInfo, privKey)
		if err != nil {
			logging.Infof("Error signing relay info: %v", err)
		}
	} else {
		logging.Infof("Not advertising hornet extension because dht_pubkey == %s", dhtPubkey)
	}

	return relayInfo
}

// buildServicesMap constructs the services map from config
// Built-in services (hornets, panel, blossom) use fixed offsets from base_port
// External services configured under server.services are advertised here
func buildServicesMap() RelayServices {
	services := make(RelayServices)

	// Get all services from config
	servicesConfig := viper.GetStringMap("server.services")

	for serviceName := range servicesConfig {
		// Check if service has a port configured (required)
		portKey := fmt.Sprintf("server.services.%s.port", serviceName)
		port := viper.GetInt(portKey)

		if port <= 0 {
			continue // Skip services without a valid port
		}

		// Build the service endpoint from config
		hostKey := fmt.Sprintf("server.services.%s.host", serviceName)
		pathKey := fmt.Sprintf("server.services.%s.path", serviceName)
		pubkeyKey := fmt.Sprintf("server.services.%s.pubkey", serviceName)
		dhtPubkeyKey := fmt.Sprintf("server.services.%s.dht_pubkey", serviceName)

		endpoint := &ServiceEndpoint{
			Host:      viper.GetString(hostKey),
			Port:      port,
			Path:      viper.GetString(pathKey),
			Pubkey:    viper.GetString(pubkeyKey),
			DHTPubkey: viper.GetString(dhtPubkeyKey),
		}

		services[serviceName] = endpoint
	}

	// Return nil if no services configured (omit from JSON)
	if len(services) == 0 {
		return nil
	}

	return services
}

func SignRelay(relay *NIP11RelayInfo, privKey *btcec.PrivateKey) error {
	relayBytes := PackRelayForSig(relay)
	hash := sha256.Sum256(relayBytes)

	signature, err := schnorr.Sign(privKey, hash[:])
	if err != nil {
		return err
	}

	if relay.HornetExtension == nil {
		relay.HornetExtension = &HornetExtension{}
	}

	relay.HornetExtension.Signature = hex.EncodeToString(signature.Serialize())
	relay.HornetExtension.LastUpdated = time.Now()
	return nil
}

func PackRelayForSig(nr *NIP11RelayInfo) []byte {
	var packed []byte

	// Pack Name
	packed = append(packed, []byte(nr.Name)...)
	packed = append(packed, 0) // null terminator

	// Pack Description
	packed = append(packed, []byte(nr.Description)...)
	packed = append(packed, 0)

	// Pack PublicKey
	pubkeyBytes, err := hex.DecodeString(nr.Pubkey)
	if err != nil {
		logging.Infof("Skipping packing invalid pubkey %s", nr.Pubkey)
	} else {
		packed = append(packed, pubkeyBytes...)
	}

	// Pack Contact
	packed = append(packed, []byte(nr.Contact)...)
	packed = append(packed, 0)

	// Pack SupportedNIPs (sorted)
	sort.Ints(nr.SupportedNIPs)
	for _, nip := range nr.SupportedNIPs {
		nipBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(nipBytes, uint32(nip))
		packed = append(packed, nipBytes...)
	}

	// Pack Software
	packed = append(packed, []byte(nr.Software)...)
	packed = append(packed, 0)

	// Pack Version
	packed = append(packed, []byte(nr.Version)...)
	packed = append(packed, 0)

	if nr.HornetExtension != nil {
		// Pack DHT public key
		packed = append(packed, []byte(nr.HornetExtension.DHTPubkey)...)
		packed = append(packed, 0) // null terminator

		// Pack LastUpdated
		unixTime := nr.HornetExtension.LastUpdated.Unix()
		timeBytes := make([]byte, 8) // Use 8 bytes for int64
		binary.BigEndian.PutUint64(timeBytes, uint64(unixTime))
		packed = append(packed, timeBytes...)
	}

	return packed
}

func processWebSocketMessage(c *websocket.Conn, challenge string, state *connectionState, store stores.Store) error {
	// Check if server is shutting down or store is closed
	if IsShuttingDown() || store.IsClosed() {
		return fmt.Errorf("server is shutting down")
	}

	_, message, err := c.ReadMessage()
	if err != nil {
		return fmt.Errorf("read error: %w", err)
	}

	// Special handling for AUTH messages
	var rawArray []interface{}
	if err := json.Unmarshal(message, &rawArray); err == nil {
		if len(rawArray) >= 2 {
			if msgType, ok := rawArray[0].(string); ok && msgType == "AUTH" {
				logging.Infof("Detected AUTH message")

				// If second element is a string, it's the initial challenge
				if challenge, ok := rawArray[1].(string); ok {
					logging.Infof("Initial AUTH challenge received: %s", challenge)
					return nil
				}

				// If second element is a map, it's the auth event
				if eventMap, ok := rawArray[1].(map[string]interface{}); ok {
					// Check if already authenticated
					if state.authenticated {
						logging.Infof("Ignoring AUTH event - connection already authenticated for pubkey: %s", state.pubkey)
						noticeMsg := nostr.NoticeEnvelope("Already authenticated")
						c.WriteJSON(noticeMsg)
						return nil
					}

					// Rate limiting: Check if too many auth attempts
					const maxAuthAttempts = 5
					const authRateLimitWindow = time.Minute

					now := time.Now()
					if state.authAttempts >= maxAuthAttempts {
						if now.Sub(state.lastAuthAttempt) < authRateLimitWindow {
							logging.Infof("Rate limiting AUTH attempts - %d attempts in last minute", state.authAttempts)
							noticeMsg := nostr.NoticeEnvelope("Too many authentication attempts. Please wait before trying again.")
							c.WriteJSON(noticeMsg)
							return nil
						}
						// Reset counter after window expires
						state.authAttempts = 0
					}

					state.authAttempts++
					state.lastAuthAttempt = now

					logging.Infof("Received AUTH event (attempt %d)", state.authAttempts)
					eventBytes, err := json.Marshal(eventMap)
					if err != nil {
						logging.Infof("Failed to marshal event map: %v", err)
						return nil
					}

					var event nostr.Event
					if err := json.Unmarshal(eventBytes, &event); err != nil {
						logging.Infof("Failed to unmarshal event: %v", err)
						return nil
					}

					authEnv := &nostr.AuthEnvelope{Event: event}
					logging.Infof("Handling AUTH event")
					handleAuthMessage(c, authEnv, challenge, state, store)
					return nil
				}
			}
		}
	}

	// For all non-AUTH messages from authenticated users, check if blocked
	if state.authenticated && state.pubkey != "" && terminateIfBlocked(c, state, store) {
		return fmt.Errorf("connection terminated: blocked pubkey")
	}

	// Parse the message
	rawMessage := nostr.ParseMessage(message)

	// Handle different message types
	switch env := rawMessage.(type) {
	case *nostr.EventEnvelope:
		handleEventMessage(c, env, state, store)
	case *nostr.ReqEnvelope:
		handleReqMessage(c, env, state, store)
	case *nostr.AuthEnvelope:
		logging.Infof("Handling AUTH message")
		handleAuthMessage(c, env, challenge, state, store)
	case *nostr.CloseEnvelope:
		handleCloseMessage(c, env)
	case *nostr.CountEnvelope:
		handleCountMessage(c, env, challenge)
	default:
		firstComma := bytes.Index(message, []byte{','})
		if firstComma == -1 {
			return nil
		}
		label := message[0:firstComma]
		logging.Infof("Unknown message type: %s", string(label))
	}

	return nil
}
