package relaylisteners

import (
	"context"
	"errors"
	"strings"
	"sync"

	searchquery "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/nbd-wtf/go-nostr"
)

const defaultQueueCapacity = 1000

var ErrConnectionClosed = errors.New("relay listener connection is closed")

// Sender delivers one already-filtered live EVENT envelope to a transport.
// The transport owns framing and must serialize this write with its ordinary replies.
type Sender func(nostr.EventEnvelope) error

// AccessControllerProvider resolves the current read policy for every dispatch so
// runtime access-control changes affect established subscriptions immediately.
type AccessControllerProvider func() eventvisibility.AccessController

type subscription struct {
	filters nostr.Filters
	cancel  context.CancelFunc
}

// Connection is a transport-neutral Nostr connection registered with the live broker.
// A WebSocket or DHT stream may own many independent subscription IDs.
type Connection struct {
	broker *Broker
	sender Sender

	mu            sync.RWMutex
	pubkey        string
	closed        bool
	subscriptions map[string]*subscription
}

// Broker owns all live Nostr subscriptions regardless of their network transport.
type Broker struct {
	mu             sync.RWMutex
	store          stores.Store
	accessProvider AccessControllerProvider
	connections    map[*Connection]struct{}

	queue     chan nostr.Event
	startOnce sync.Once
}

func NewBroker(queueCapacity int) *Broker {
	if queueCapacity <= 0 {
		queueCapacity = defaultQueueCapacity
	}
	return &Broker{
		connections: make(map[*Connection]struct{}),
		queue:       make(chan nostr.Event, queueCapacity),
	}
}

// Configure updates the canonical visibility dependencies and starts the bounded
// asynchronous fan-out worker exactly once.
func (broker *Broker) Configure(store stores.Store, accessProvider AccessControllerProvider) {
	broker.mu.Lock()
	broker.store = store
	broker.accessProvider = accessProvider
	broker.mu.Unlock()
	broker.startOnce.Do(func() {
		go func() {
			for event := range broker.queue {
				broker.Dispatch(&event)
			}
		}()
	})
}

func (broker *Broker) Register(sender Sender) *Connection {
	connection := &Connection{
		broker:        broker,
		sender:        sender,
		subscriptions: make(map[string]*subscription),
	}
	broker.mu.Lock()
	broker.connections[connection] = struct{}{}
	broker.mu.Unlock()
	return connection
}

// Notify queues a stored event without blocking the publishing handler. False means
// the bounded queue was full and the live notification was deliberately dropped.
func (broker *Broker) Notify(event *nostr.Event) bool {
	if event == nil {
		return false
	}
	select {
	case broker.queue <- *event:
		return true
	default:
		logging.Infof("Warning: relay listener queue full, dropping notification for event %s", event.ID)
		return false
	}
}

// Dispatch performs synchronous fan-out. Production notifications reach this through
// Notify; tests use it directly to prove matching and lifecycle semantics.
func (broker *Broker) Dispatch(event *nostr.Event) {
	if event == nil {
		return
	}
	broker.mu.RLock()
	store := broker.store
	accessProvider := broker.accessProvider
	connections := make([]*Connection, 0, len(broker.connections))
	for connection := range broker.connections {
		connections = append(connections, connection)
	}
	broker.mu.RUnlock()

	blocked, pending := eventvisibility.LoadModerationStatus(store, []*nostr.Event{event})
	var accessControl eventvisibility.AccessController
	if accessProvider != nil {
		accessControl = accessProvider()
	}
	for _, connection := range connections {
		connection.dispatch(event, store, accessControl, blocked[event.ID], pending[event.ID])
	}
}

func (connection *Connection) Subscribe(id string, filters nostr.Filters, cancel context.CancelFunc) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("subscription id is required")
	}
	if cancel == nil {
		cancel = func() {}
	}
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		cancel()
		return ErrConnectionClosed
	}
	previous := connection.subscriptions[id]
	connection.subscriptions[id] = &subscription{filters: append(nostr.Filters(nil), filters...), cancel: cancel}
	connection.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}
	return nil
}

func (connection *Connection) RemoveSubscription(id string) bool {
	connection.mu.Lock()
	current, ok := connection.subscriptions[id]
	if ok {
		delete(connection.subscriptions, id)
	}
	connection.mu.Unlock()
	if ok {
		current.cancel()
	}
	return ok
}

// Authenticate changes the identity used by the same visibility policy as historical
// queries. An empty identity restores anonymous read behavior.
func (connection *Connection) Authenticate(pubkey string) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return ErrConnectionClosed
	}
	connection.pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	return nil
}

func (connection *Connection) Close() {
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		return
	}
	connection.closed = true
	subscriptions := connection.subscriptions
	connection.subscriptions = make(map[string]*subscription)
	connection.mu.Unlock()

	connection.broker.mu.Lock()
	delete(connection.broker.connections, connection)
	connection.broker.mu.Unlock()
	for _, current := range subscriptions {
		current.cancel()
	}
}

func (connection *Connection) dispatch(event *nostr.Event, store stores.Store, accessControl eventvisibility.AccessController, blocked, pending bool) {
	connection.mu.RLock()
	if connection.closed {
		connection.mu.RUnlock()
		return
	}
	pubkey := connection.pubkey
	subscriptions := make(map[string]*subscription, len(connection.subscriptions))
	for id, current := range connection.subscriptions {
		subscriptions[id] = current
	}
	sender := connection.sender
	connection.mu.RUnlock()

	policy := eventvisibility.NewPolicy(store, pubkey, accessControl)
	for id, snapshot := range subscriptions {
		matched := false
		for _, filter := range snapshot.filters {
			if LiveFilterMatches(event, filter, policy, blocked, pending) {
				matched = true
				break
			}
		}
		if !matched || sender == nil {
			continue
		}

		// CLOSE, connection teardown, authentication changes, and same-id REQ replacement
		// may race with the policy work above. Hold the read lock from generation validation
		// through delivery, so lifecycle mutation cannot complete and then receive a stale EVENT.
		connection.mu.RLock()
		current, active := connection.subscriptions[id]
		active = active && !connection.closed && current == snapshot && connection.pubkey == pubkey
		if !active {
			connection.mu.RUnlock()
			continue
		}
		err := sender(nostr.EventEnvelope{SubscriptionID: &id, Event: *event})
		connection.mu.RUnlock()
		if err != nil {
			logging.Infof("Error notifying relay listener: %v", err)
			connection.Close()
			return
		}
	}
}

func LiveFilterMatches(event *nostr.Event, filter nostr.Filter, policy eventvisibility.Policy, blocked, pending bool) bool {
	if !searchquery.EventMatchesFilter(event, filter) {
		return false
	}
	parsedSearch := searchquery.ParseSearchQuery(filter.Search)
	return policy.CanExpose(event, parsedSearch.IsSpamIncluded(), blocked, pending)
}

var defaultBroker = NewBroker(defaultQueueCapacity)

func Configure(store stores.Store, accessProvider AccessControllerProvider) {
	defaultBroker.Configure(store, accessProvider)
}

func Register(sender Sender) *Connection {
	return defaultBroker.Register(sender)
}

func Notify(event *nostr.Event) bool {
	return defaultBroker.Notify(event)
}

func Dispatch(event *nostr.Event) {
	defaultBroker.Dispatch(event)
}
