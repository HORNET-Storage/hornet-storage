package websocket

import (
	"context"

	jsoniter "github.com/json-iterator/go"

	"github.com/gofiber/contrib/websocket"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
)

func handleReqMessage(c *websocket.Conn, env *nostr.ReqEnvelope, state *connectionState, store stores.Store) {
	// If the user is authenticated, check if they're blocked
	if state.authenticated && terminateIfBlocked(c, state, store) {
		return
	}

	// Anonymous read-only access is allowed (no authentication required)
	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	handler := lib_nostr.GetHandler("filter")

	if handler != nil {
		_, cancelFunc := context.WithCancel(context.Background())

		setListener(env.SubscriptionID, c, env.Filters, cancelFunc)

		// If the connection authenticated before this REQ, sync that state
		// to the listener data so live notifications reach this subscriber.
		if state.authenticated {
			AuthenticateConnection(c, state.pubkey)
		}

		read := func() ([]byte, error) {
			// Create a wrapper structure that includes both the request and authentication info
			wrapper := struct {
				Request         *nostr.ReqEnvelope `json:"request"`
				AuthPubkey      string             `json:"auth_pubkey"`
				IsAuthenticated bool               `json:"is_authenticated"`
			}{
				Request:         env,
				AuthPubkey:      state.pubkey,
				IsAuthenticated: state.authenticated,
			}
			return json.Marshal(wrapper)
		}

		write := func(messageType string, params ...interface{}) {
			response := lib_nostr.BuildResponse(messageType, params)
			if len(response) > 0 {
				handleIncomingMessage(c, response)
			}
		}

		handler(read, write)
	}
}
