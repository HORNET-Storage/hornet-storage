package kind0

import (
	"strings"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/btcsuite/btcd/btcec/v2"
	jsoniter "github.com/json-iterator/go"
	"github.com/nbd-wtf/go-nostr"
)

// NIP24Tags represents the tags defined in NIP-24
type NIP24Tags struct {
	ReferenceURLs []string // 'r' tags - web URLs the event is referring to
	ExternalIDs   []string // 'i' tags - external IDs the event is referring to
	Titles        []string // 'title' tags - names of NIP-51 sets, NIP-52 calendar events, etc.
	Hashtags      []string // 't' tags - hashtags (always lowercase)
}

// ExtractNIP24Tags extracts the tags defined in NIP-24 from a Nostr event
// These tags have the following meanings:
// - 'r': a web URL the event is referring to in some way
// - 'i': an external id the event is referring to in some way
// - 'title': name of NIP-51 sets, NIP-52 calendar event, NIP-53 live event or NIP-99 listing
// - 't': a hashtag. The value MUST be a lowercase string
func ExtractNIP24Tags(event *nostr.Event) *NIP24Tags {
	result := &NIP24Tags{
		ReferenceURLs: []string{},
		ExternalIDs:   []string{},
		Titles:        []string{},
		Hashtags:      []string{},
	}

	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "r": // Web URL reference
				result.ReferenceURLs = append(result.ReferenceURLs, tag[1])
			case "i": // External ID reference
				result.ExternalIDs = append(result.ExternalIDs, tag[1])
			case "title": // Title for various NIP types
				result.Titles = append(result.Titles, tag[1])
			case "t": // Hashtag (must be lowercase)
				// Ensure hashtag is lowercase as per NIP-24 specification
				hashtag := strings.ToLower(tag[1])
				result.Hashtags = append(result.Hashtags, hashtag)
			}
		}
	}

	return result
}

// ValidateNIP24Tags validates that the NIP-24 tags in an event follow the specification
// Currently, this only checks that hashtags are lowercase
func ValidateNIP24Tags(event *nostr.Event) bool {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "t" {
			// Hashtag must be lowercase
			if tag[1] != strings.ToLower(tag[1]) {
				logging.Infof("Warning: Hashtag '%s' is not lowercase as required by NIP-24", tag[1])
				return false
			}
		}
	}
	return true
}

// BuildKind0Handler creates a handler for kind 0 events
func BuildKind0Handler(store stores.Store, relayPrivKey *btcec.PrivateKey) func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
	handler := func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
		// Use Jsoniter for JSON operations
		var json = jsoniter.ConfigCompatibleWithStandardLibrary

		// Read data from the stream
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
		success := lib_nostr.ValidateEvent(write, env, 0)
		if !success {
			return
		}

		// Check for NIP-24 tags - all NIP-24 tags are optional
		hasNIP24Tags := false
		for _, tag := range env.Event.Tags {
			if len(tag) >= 2 {
				switch tag[0] {
				case "r", "i", "title", "t":
					hasNIP24Tags = true
				}
			}
			if hasNIP24Tags {
				break
			}
		}

		// Only validate and extract NIP-24 tags if they are present
		if hasNIP24Tags {
			// Validate NIP-24 tags if present (but still process the event even if tags are invalid)
			if !ValidateNIP24Tags(&env.Event) {
				logging.Infof("Warning: Event %s contains invalid NIP-24 tags, but will still be processed", env.Event.ID)
			}

			// Extract and log NIP-24 tags for debugging
			nip24Tags := ExtractNIP24Tags(&env.Event)
			logging.Infof("NIP-24 tags for event %s: URLs=%v, ExternalIDs=%v, Titles=%v, Hashtags=%v",
				env.Event.ID, nip24Tags.ReferenceURLs, nip24Tags.ExternalIDs,
				nip24Tags.Titles, nip24Tags.Hashtags)
		}

		// Retrieve existing kind 0 events for the pubkey
		filter := nostr.Filter{
			Authors: []string{env.Event.PubKey},
			Kinds:   []int{0},
		}
		existingEvents, err := store.QueryEvents(filter)
		if err != nil {
			logging.Infof("Error querying existing kind 0 events: %v", err)
			write("NOTICE", "Error querying existing events")
			return
		}

		// Delete existing kind 0 events if any (NIP-01 replaceable event semantics)
		if len(existingEvents) > 0 {
			for _, oldEvent := range existingEvents {
				if oldEvent.CreatedAt > env.Event.CreatedAt {
					// Existing event is newer — reject the incoming event
					write("OK", env.Event.ID, false, "blocked: existing profile is newer")
					return
				}
				if err := store.DeleteEvent(oldEvent.ID); err != nil {
					logging.Infof("Error deleting old kind 0 event %s: %v", oldEvent.ID, err)
					write("NOTICE", "Error deleting old kind 0 event %s: %v", oldEvent.ID, err)
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
