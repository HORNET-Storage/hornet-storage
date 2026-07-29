package websocket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"

	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/relaylisteners"
	"github.com/gofiber/contrib/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/puzpuzpuz/xsync/v3"
)

// WebSocket connections are adapters into the transport-neutral live listener broker.
var listeners = xsync.NewMapOf[*websocket.Conn, *relaylisteners.Connection]()

// Per-connection write mutexes serialize historical replies, authentication replies,
// and asynchronous live EVENT fan-out on the same WebSocket.
var connWriteMu = xsync.NewMapOf[*websocket.Conn, *sync.Mutex]()

// notificationStore remains the relay-info source for advertising NIP-50 readiness.
var (
	notificationStore   stores.Store
	notificationStoreMu sync.RWMutex
)

var globalChallenge atomic.Value

const challengeLength = 32

func getConnWriteMutex(ws *websocket.Conn) *sync.Mutex {
	mu, _ := connWriteMu.LoadOrCompute(ws, func() *sync.Mutex {
		return &sync.Mutex{}
	})
	return mu
}

// StartNotificationProcessor configures the shared live broker. The broker owns one
// bounded asynchronous queue and serves both WebSocket and DHT stream connections.
func StartNotificationProcessor(store stores.Store) {
	notificationStoreMu.Lock()
	notificationStore = store
	notificationStoreMu.Unlock()
	relaylisteners.Configure(store, func() eventvisibility.AccessController {
		return GetAccessControl()
	})
}

// processNotification is retained as a synchronous test seam.
func processNotification(event *nostr.Event) {
	relaylisteners.Dispatch(event)
}

func liveFilterMatches(event *nostr.Event, filter nostr.Filter, policy eventvisibility.Policy, blocked, pending bool) bool {
	return relaylisteners.LiveFilterMatches(event, filter, policy, blocked, pending)
}

// notifyListeners queues an accepted event for all matching WebSocket and DHT listeners.
func notifyListeners(event *nostr.Event) {
	relaylisteners.Notify(event)
}

// NotifyListeners exposes the common live ingress to non-WebSocket transports.
func NotifyListeners(event *nostr.Event) {
	notifyListeners(event)
}

func listenerConnection(ws *websocket.Conn) *relaylisteners.Connection {
	connection, _ := listeners.LoadOrCompute(ws, func() *relaylisteners.Connection {
		return relaylisteners.Register(func(envelope nostr.EventEnvelope) error {
			return sendWebSocketMessage(ws, envelope)
		})
	})
	return connection
}

// setListener installs or replaces a subscription ID while preserving connection identity.
func setListener(id string, ws *websocket.Conn, filters nostr.Filters, cancel context.CancelFunc) {
	if err := listenerConnection(ws).Subscribe(id, filters, cancel); err != nil {
		cancel()
	}
}

func removeListenerId(ws *websocket.Conn, id string) bool {
	if connection, ok := listeners.Load(ws); ok {
		return connection.RemoveSubscription(id)
	}
	return false
}

func removeListener(ws *websocket.Conn) {
	if connection, ok := listeners.LoadAndDelete(ws); ok {
		connection.Close()
	}
	connWriteMu.Delete(ws)
}

func GetListenerChallenge(ws *websocket.Conn) (*string, error) {
	if _, ok := listeners.Load(ws); !ok {
		return nil, fmt.Errorf("no listeners found for this WebSocket connection")
	}
	challenge := getGlobalChallenge()
	return &challenge, nil
}

func AuthenticateConnection(ws *websocket.Conn, pubkey string) error {
	connection, ok := listeners.Load(ws)
	if !ok {
		return fmt.Errorf("no listeners found for this WebSocket connection")
	}
	return connection.Authenticate(pubkey)
}

func generateGlobalChallenge() (string, error) {
	bytes := make([]byte, challengeLength)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %v", err)
	}
	challenge := hex.EncodeToString(bytes)
	globalChallenge.Store(challenge)
	return challenge, nil
}

func getGlobalChallenge() string {
	val := globalChallenge.Load()
	if val == nil {
		challenge, err := generateGlobalChallenge()
		if err != nil {
			return ""
		}
		return challenge
	}
	return val.(string)
}

func InitGlobalChallenge() error {
	_, err := generateGlobalChallenge()
	return err
}
