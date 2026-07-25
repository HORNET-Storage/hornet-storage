package kind5

import (
	"fmt"

	jsoniter "github.com/json-iterator/go"

	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/nbd-wtf/go-nostr"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
)

func BuildKind5Handler(store stores.Store) func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
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
		success := lib_nostr.ValidateEvent(write, env, 5)
		if !success {
			return
		}

		if err := applyDeletionAndStoreTombstone(store, &env.Event); err != nil {
			logging.Infof("Failed to process deletion event %s: %v", env.Event.ID, err)
			write("NOTICE", err.Error())
			return
		}
		write("OK", env.Event.ID, true, "Deletion processed and tombstone stored")

	}

	return handler
}

func applyDeletionAndStoreTombstone(store stores.Store, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("deletion event is required")
	}
	// Apply deletion requests to locally stored source events. The signed kind-5
	// event is retained below as durable deletion evidence so clients and relay
	// synchronization can verify that the source was intentionally removed.
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "e" {
			continue
		}
		eventID := tag[1]
		pubKey, err := extractPubKeyFromEventID(store, eventID)
		if err != nil {
			logging.Infof("Failed to extract public key for event %s: %v", eventID, err)
			continue
		}
		if pubKey != event.PubKey {
			logging.Infof("Public key mismatch for event %s, deletion request ignored", eventID)
			continue
		}
		if err := store.DeleteEvent(eventID); err != nil {
			return fmt.Errorf("failed to delete event %s: %w", eventID, err)
		}
	}

	if err := store.StoreEvent(event); err != nil {
		return fmt.Errorf("failed to preserve deletion event: %w", err)
	}
	return nil
}

func extractPubKeyFromEventID(store stores.Store, eventID string) (string, error) {
	events, err := store.QueryEvents(nostr.Filter{
		IDs: []string{eventID},
	})

	if err != nil {
		return "", err
	}

	if len(events) == 0 {
		return "", fmt.Errorf("no events found for ID: %s", eventID)
	}

	event := events[0]
	return event.PubKey, nil
}
