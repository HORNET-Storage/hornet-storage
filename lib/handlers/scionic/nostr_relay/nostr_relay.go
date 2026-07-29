package nostr_relay

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	jsoniter "github.com/json-iterator/go"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	nostr_auth "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/auth"
	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/relaylisteners"
	ws "github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
	"github.com/HORNET-Storage/hornet-storage/services/push"

	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	hsListener "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr/hyperswarm"
)

type dhtAuthState struct {
	pubkey        string
	authenticated bool
}

// AddNostrRelayHandler registers the /nostr protocol on the hyperswarm listener.
// This creates a single bidirectional stream per client that speaks full Nostr
// protocol (EVENT, REQ, CLOSE, AUTH, COUNT) — exactly like a WebSocket connection
// but over the DHT.
func AddNostrRelayHandler(listener *hsListener.HyperswarmListener, store stores.Store) {
	relaylisteners.Configure(store, func() eventvisibility.AccessController {
		return ws.GetAccessControl()
	})
	listener.SetStreamHandler("/nostr", buildNostrStreamHandler(store))
}

// buildNostrStreamHandler returns a stream handler that processes Nostr protocol
// messages in a loop, dispatching to the same handlers the WebSocket server uses.
func buildNostrStreamHandler(store stores.Store) hsListener.StreamHandler {
	return func(stream lib_types.Stream) {
		defer stream.Close()
		logging.Info("/nostr: new DHT relay connection")

		// Historical replies and asynchronous live fan-out share the same full-duplex
		// stream, so every write must pass through one serialization boundary.
		var streamWriteMu sync.Mutex
		writeBytes := func(payload []byte) error {
			streamWriteMu.Lock()
			defer streamWriteMu.Unlock()
			_, err := stream.Write(payload)
			return err
		}

		challenge, err := generateChallenge()
		if err != nil {
			logging.Errorf("/nostr: failed to generate AUTH challenge: %v", err)
			return
		}
		authMsg := lib_nostr.BuildResponse("AUTH", challenge)
		if err := writeBytes(authMsg); err != nil {
			logging.Errorf("/nostr: failed to send AUTH challenge: %v", err)
			return
		}

		var json = jsoniter.ConfigCompatibleWithStandardLibrary
		writeFn := func(messageType string, params ...interface{}) {
			flat := lib_nostr.ExtractInterfaceValues(params)
			var out []byte
			switch messageType {
			case "EVENT":
				if len(flat) >= 2 {
					subID, _ := flat[0].(string)
					eventStr, _ := flat[1].(string)
					var event nostr.Event
					if err := json.Unmarshal([]byte(eventStr), &event); err == nil {
						envelope := nostr.EventEnvelope{SubscriptionID: &subID, Event: event}
						if encoded, err := envelope.MarshalJSON(); err == nil {
							out = append(encoded, '\n')
						}
					}
				}
			case "EOSE":
				if len(flat) >= 1 {
					subID, _ := flat[0].(string)
					if encoded, err := nostr.EOSEEnvelope(subID).MarshalJSON(); err == nil {
						out = append(encoded, '\n')
					}
				}
			case "OK":
				if len(flat) >= 2 {
					eventID, _ := flat[0].(string)
					accepted, _ := flat[1].(bool)
					reason := ""
					if len(flat) >= 3 {
						reason, _ = flat[2].(string)
					}
					envelope := nostr.OKEnvelope{EventID: eventID, OK: accepted, Reason: reason}
					if encoded, err := envelope.MarshalJSON(); err == nil {
						out = append(encoded, '\n')
					}
				}
			case "NOTICE":
				if len(flat) >= 1 {
					message, _ := flat[0].(string)
					if encoded, err := nostr.NoticeEnvelope(message).MarshalJSON(); err == nil {
						out = append(encoded, '\n')
					}
				}
			case "AUTH":
				if len(flat) >= 1 {
					value, _ := flat[0].(string)
					envelope := nostr.AuthEnvelope{Challenge: &value}
					if encoded, err := envelope.MarshalJSON(); err == nil {
						out = append(encoded, '\n')
					}
				}
			case "CLOSED":
				if len(flat) >= 1 {
					subID, _ := flat[0].(string)
					reason := ""
					if len(flat) >= 2 {
						reason, _ = flat[1].(string)
					}
					envelope := nostr.ClosedEnvelope{SubscriptionID: subID, Reason: reason}
					if encoded, err := envelope.MarshalJSON(); err == nil {
						out = append(encoded, '\n')
					}
				}
			default:
				out = lib_nostr.BuildResponse(messageType, params)
			}
			if len(out) > 0 {
				if err := writeBytes(out); err != nil {
					logging.Errorf("/nostr: write error sending %s: %v", messageType, err)
				}
			}
		}

		liveConnection := relaylisteners.Register(func(envelope nostr.EventEnvelope) error {
			encoded, err := envelope.MarshalJSON()
			if err != nil {
				return err
			}
			return writeBytes(append(encoded, '\n'))
		})
		defer liveConnection.Close()

		authState := &dhtAuthState{}
		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			envelope := nostr.ParseMessage(line)
			if envelope == nil {
				logging.Infof("/nostr: unparseable message (%d bytes), skipping", len(line))
				continue
			}

			switch env := envelope.(type) {
			case *nostr.EventEnvelope:
				if handleEvent(env, writeFn, store, json) {
					relaylisteners.Notify(&env.Event)
					if pushService := push.GetGlobalPushService(); pushService != nil {
						pushService.ProcessEvent(&env.Event)
					}
				}
			case *nostr.ReqEnvelope:
				handleReq(env, writeFn, json, authState, liveConnection)
			case *nostr.CountEnvelope:
				handleCount(env, writeFn, json, authState)
			case *nostr.CloseEnvelope:
				subID := string(*env)
				liveConnection.RemoveSubscription(subID)
				writeFn("CLOSED", subID, "Subscription closed successfully.")
			case *nostr.AuthEnvelope:
				result, message, ok := nostr_auth.AuthenticateEvent(&env.Event, challenge, store, ws.GetAccessControl())
				if ok {
					authState.pubkey = result.PubKey
					authState.authenticated = true
					if err := liveConnection.Authenticate(result.PubKey); err != nil {
						ok = false
						message = err.Error()
					}
				}
				writeFn("OK", env.Event.ID, ok, message)
			default:
				logging.Infof("/nostr: unhandled envelope type %T", envelope)
			}
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			logging.Debugf("/nostr: stream read error: %v", err)
		}
		logging.Debug("/nostr: DHT relay connection closed")
	}
}

// handleEvent dispatches an EVENT message to the appropriate kind handler.
func handleEvent(env *nostr.EventEnvelope, writeFn lib_nostr.KindWriter, store stores.Store, json jsoniter.API) bool {
	if store != nil {
		isBlocked, err := store.IsBlockedPubkey(env.Event.PubKey)
		if err != nil {
			logging.Debugf("/nostr: error checking blocked pubkey: %v", err)
		} else if isBlocked {
			writeFn("OK", env.Event.ID, false, "Event rejected: Pubkey is blocked")
			return false
		}
	}
	if accessControl := ws.GetAccessControl(); accessControl != nil {
		if err := accessControl.CanWriteEvent(&env.Event, store); err != nil {
			writeFn("OK", env.Event.ID, false, "Event rejected: Write access denied")
			return false
		}
	}

	accepted := false
	trackedWrite := func(messageType string, params ...interface{}) {
		flat := lib_nostr.ExtractInterfaceValues(params)
		if messageType == "OK" && len(flat) >= 2 {
			eventID, _ := flat[0].(string)
			ok, _ := flat[1].(bool)
			if eventID == env.Event.ID && ok {
				accepted = true
			}
		}
		writeFn(messageType, params...)
	}

	handler := lib_nostr.GetHandler(fmt.Sprintf("kind/%d", env.Kind))
	if handler != nil {
		if !lib_nostr.IsKindAllowed(env.Kind) {
			writeFn("OK", env.Event.ID, false, fmt.Sprintf("Kind %d not allowed", env.Kind))
			return false
		}
		read := func() ([]byte, error) { return json.Marshal(env) }
		handler(read, trackedWrite)
		return accepted
	}
	if lib_nostr.IsKindAllowed(env.Kind) {
		universalHandler := lib_nostr.GetHandler("universal")
		if universalHandler == nil {
			writeFn("OK", env.Event.ID, false, "Universal handler not available")
			return false
		}
		read := func() ([]byte, error) { return json.Marshal(env) }
		universalHandler(read, trackedWrite)
		return accepted
	}
	writeFn("OK", env.Event.ID, false, fmt.Sprintf("Unregistered kind %d not allowed", env.Kind))
	return false
}

// handleReq dispatches a REQ message to the filter handler.
func handleReq(env *nostr.ReqEnvelope, writeFn lib_nostr.KindWriter, json jsoniter.API, authState *dhtAuthState, connection *relaylisteners.Connection) {
	handler := lib_nostr.GetHandler("filter")
	if handler == nil {
		writeFn("NOTICE", "Filter handler not available")
		return
	}
	_, cancel := context.WithCancel(context.Background())
	if err := connection.Subscribe(env.SubscriptionID, env.Filters, cancel); err != nil {
		cancel()
		writeFn("CLOSED", env.SubscriptionID, err.Error())
		return
	}

	read := func() ([]byte, error) {
		wrapper := struct {
			Request         *nostr.ReqEnvelope `json:"request"`
			AuthPubkey      string             `json:"auth_pubkey"`
			IsAuthenticated bool               `json:"is_authenticated"`
		}{Request: env, AuthPubkey: authState.pubkey, IsAuthenticated: authState.authenticated}
		return json.Marshal(wrapper)
	}
	requestWrite := func(messageType string, params ...interface{}) {
		flat := lib_nostr.ExtractInterfaceValues(params)
		if messageType == "CLOSED" && len(flat) > 0 {
			if subID, _ := flat[0].(string); subID == env.SubscriptionID {
				connection.RemoveSubscription(subID)
			}
		}
		writeFn(messageType, params...)
	}
	handler(read, requestWrite)
}

// handleCount dispatches COUNT through the same authenticated visibility context as REQ.
func handleCount(env *nostr.CountEnvelope, writeFn lib_nostr.KindWriter, json jsoniter.API, authState *dhtAuthState) {
	handler := lib_nostr.GetHandler("count")
	if handler == nil {
		writeFn("NOTICE", "Count handler not available")
		return
	}
	read := func() ([]byte, error) {
		wrapper := struct {
			Request         *nostr.CountEnvelope `json:"request"`
			AuthPubkey      string               `json:"auth_pubkey"`
			IsAuthenticated bool                 `json:"is_authenticated"`
		}{Request: env, AuthPubkey: authState.pubkey, IsAuthenticated: authState.authenticated}
		return json.Marshal(wrapper)
	}
	handler(read, writeFn)
}

func generateChallenge() (string, error) {
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return "", err
	}
	return hex.EncodeToString(challenge), nil
}
