package websocket

import (
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	nostr_auth "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/auth"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
)

const (
	AddressStatusAvailable = "available"
	AddressStatusAllocated = "allocated"
	AddressStatusUsed      = "used"
)

func handleAuthMessage(c *websocket.Conn, env *nostr.AuthEnvelope, challenge string, state *connectionState, store stores.Store) {
	write := func(messageType string, params ...interface{}) {
		response := lib_nostr.BuildResponse(messageType, params)
		if len(response) > 0 {
			handleIncomingMessage(c, response)
		}
	}

	logging.Infof("Handling auth message for user with pubkey: %s", env.Event.PubKey)

	result, message, ok := nostr_auth.AuthenticateEvent(&env.Event, challenge, store, GetAccessControl())
	if !ok {
		write("OK", env.Event.ID, false, message)
		return
	}

	write("OK", env.Event.ID, true, message)

	// Store the pubkey in connection state for future block checks
	state.pubkey = result.PubKey
	state.authenticated = true
	state.blockedCheck = time.Now()

	// Sync auth state to listener data so live subscription notifications
	// are dispatched to this connection. Safe to ignore the error — it just
	// means no subscriptions exist yet; the next REQ will pick up the
	// auth state from connectionState.
	AuthenticateConnection(c, state.pubkey)
}
