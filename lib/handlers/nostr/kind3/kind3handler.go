package kind3

import (
	"fmt"

	jsoniter "github.com/json-iterator/go"

	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
)

func BuildKind3Handler(store stores.Store) func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
	handler := func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
		var json = jsoniter.ConfigCompatibleWithStandardLibrary

		data, err := read()
		if err != nil {
			write("NOTICE", "Error reading from stream.")
			return
		}

		// Unmarshal the received data into a Nostr event
		var env nostr.EventEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			write("NOTICE", "Error unmarshaling event.")
			return
		}

		// Check relay settings for allowed events whilst also verifying signatures and kind number
		success := lib_nostr.ValidateEvent(write, env, 3)
		if !success {
			return
		}

		// Retrieve existing contact list events for the user
		filter := nostr.Filter{
			Authors: []string{env.Event.PubKey},
			Kinds:   []int{3},
		}
		existingEvents, err := store.QueryEvents(filter)
		if err != nil {
			logging.Infof("Error querying existing contact list events: %v", err)
			write("NOTICE", fmt.Sprintf("Error querying existing contact list events: %v", err))
			return
		}

		// If there are existing events, check timestamps (NIP-01 replaceable event semantics)
		if len(existingEvents) > 0 {
			for _, oldEvent := range existingEvents {
				if oldEvent.CreatedAt > env.Event.CreatedAt {
					// Existing event is newer — reject the incoming event
					write("OK", env.Event.ID, false, "blocked: existing contact list is newer")
					return
				}
				if err := store.DeleteEvent(oldEvent.ID); err != nil {
					logging.Infof("Error deleting old contact list event %s: %v", oldEvent.ID, err)
				}
			}
		}

		// Store the new event
		if err := store.StoreEvent(&env.Event); err != nil {
			write("NOTICE", "Failed to store the event")
			return
		}

		// Successfully processed event
		write("OK", env.Event.ID, true, "Event stored successfully")
	}
	return handler
}
