package websocket

import (
	jsoniter "github.com/json-iterator/go"

	"github.com/gofiber/contrib/websocket"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
)

func handleCountMessage(c *websocket.Conn, env *nostr.CountEnvelope, challenge string) {
	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	handler := lib_nostr.GetHandler("count")

	if handler != nil {

		response := lib_nostr.BuildResponse("AUTH", challenge)
		if len(response) > 0 {
			handleIncomingMessage(c, response)
		}

		read := func() ([]byte, error) {
			return json.Marshal(env)
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
