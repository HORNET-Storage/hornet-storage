package websocket

import (
	"fmt"

	"github.com/gofiber/contrib/websocket"
	jsoniter "github.com/json-iterator/go"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/services/push"
)

func handleEventMessage(c *websocket.Conn, env *nostr.EventEnvelope, _ *connectionState, store stores.Store) {
	// Always check if the event is from a blocked pubkey regardless of authentication
	// Note: We use the pubkey from the event itself, not the connection state,
	// as events could be relayed from other pubkeys or unauthenticated users
	write := func(messageType string, params ...interface{}) {
		response := lib_nostr.BuildResponse(messageType, params)
		if len(response) > 0 {
			handleIncomingMessage(c, response)
		}
	}

	if store != nil {
		isBlocked, err := store.IsBlockedPubkey(env.Event.PubKey)
		if err != nil {
			logging.Infof("Error checking if pubkey is blocked: %v", err)
		} else if isBlocked {
			logging.Infof("Rejected event from blocked pubkey: %s", env.Event.PubKey)

			// Notify the client that their event was rejected
			write("OK", env.Event.ID, false, "Event rejected: Pubkey is blocked")
			return
		}
	}

	// Check write access permissions using H.O.R.N.E.T Allowed Users system
	if accessControl := GetAccessControl(); accessControl != nil {
		err := accessControl.CanWriteEvent(&env.Event, store)
		if err != nil {
			logging.Infof("Write access denied for pubkey: %s", env.Event.PubKey)
			write("OK", env.Event.ID, false, "Event rejected: Write access denied")
			return
		}
	}

	// Try to get specific handler for this kind
	handler := lib_nostr.GetHandler(fmt.Sprintf("kind/%d", env.Kind))

	if handler != nil {
		// We have a specific handler for this registered kind
		// Check if it's allowed by the whitelist
		if !lib_nostr.IsKindAllowed(env.Kind) {
			logging.Infof("Rejected event: kind %d not in whitelist", env.Kind)
			write("OK", env.Event.ID, false, fmt.Sprintf("Kind %d not allowed", env.Kind))
			return
		}
		// Use the specific handler
		handleEventWithHandler(c, env, handler)
	} else if lib_nostr.IsKindAllowed(env.Kind) {
		universalHandler := lib_nostr.GetHandler("universal")
		if universalHandler != nil {
			logging.Infof("Handling allowed kind %d with universal handler", env.Kind)
			handleEventWithHandler(c, env, universalHandler)
		} else {
			write("OK", env.Event.ID, false, "Universal handler not available")
		}
	} else {
		logging.Infof("Rejected kind %d (not allowed by event filtering config)", env.Kind)
		write("OK", env.Event.ID, false, fmt.Sprintf("Unregistered kind %d not allowed", env.Kind))
	}
}

// handleEventWithHandler processes an event with the given handler.
func handleEventWithHandler(c *websocket.Conn, env *nostr.EventEnvelope, handler func(lib_nostr.KindReader, lib_nostr.KindWriter)) {
	var json = jsoniter.ConfigCompatibleWithStandardLibrary

	read := func() ([]byte, error) {
		return json.Marshal(env)
	}

	accepted := false
	write := func(messageType string, params ...interface{}) {
		flat := lib_nostr.ExtractInterfaceValues(params)
		if messageType == "OK" && len(flat) >= 2 {
			eventID, _ := flat[0].(string)
			ok, _ := flat[1].(bool)
			if eventID == env.Event.ID && ok {
				accepted = true
			}
		}
		response := lib_nostr.BuildResponse(messageType, params)
		if len(response) > 0 {
			handleIncomingMessage(c, response)
		}
	}

	// A live notification must only be emitted after the event handler has
	// confirmed persistence. This keeps WebSocket and DHT publication semantics
	// identical and prevents rejected events from leaking into subscriptions.
	handler(read, write)
	if !accepted {
		return
	}

	notifyListeners(&env.Event)

	if pushService := push.GetGlobalPushService(); pushService != nil {
		pushService.ProcessEvent(&env.Event)
	}
}
