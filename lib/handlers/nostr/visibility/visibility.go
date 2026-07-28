package visibility

import (
	"reflect"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10010"
	searchquery "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
)

// AccessController is the read-side capability required by the visibility policy.
type AccessController interface {
	CanReadEvent(*nostr.Event, string, stores.Store) error
}

// Policy applies the same access-control, private-kind, moderation, and per-user
// mute rules to historical query results and live subscription fan-out.
type Policy struct {
	store             stores.Store
	connPubkey        string
	accessControl     AccessController
	moderationEnabled bool
	strictModeration  bool
	mutesEnabled      bool
	muteWords         []string
}

func NewPolicy(store stores.Store, connPubkey string, accessControl AccessController) Policy {
	// An interface containing a nil *AccessControl is itself non-nil. Normalize typed
	// nil implementations here so every historical and live policy remains safe when
	// access control has not been configured.
	if accessControl != nil {
		value := reflect.ValueOf(accessControl)
		if value.Kind() == reflect.Ptr && value.IsNil() {
			accessControl = nil
		}
	}

	policy := Policy{
		store:             store,
		connPubkey:        connPubkey,
		accessControl:     accessControl,
		moderationEnabled: viper.GetBool("content_filtering.image_moderation.enabled"),
		strictModeration:  viper.GetString("event_filtering.moderation_mode") != "passive",
	}
	if connPubkey != "" {
		if preference, err := kind10010.GetUserFilterPreference(store, connPubkey); err == nil && preference.Enabled {
			policy.mutesEnabled = true
			policy.muteWords = append([]string(nil), preference.MuteWords...)
		}
	}
	return policy
}

// LoadModerationStatus batches the canonical moderation lookups once. Live
// callers can share the returned maps across every listener for the same event.
func LoadModerationStatus(store stores.Store, events []*nostr.Event) (map[string]bool, map[string]bool) {
	blocked := make(map[string]bool)
	pending := make(map[string]bool)
	if store == nil || !viper.GetBool("content_filtering.image_moderation.enabled") || len(events) == 0 {
		return blocked, pending
	}

	eventIDs := make([]string, 0, len(events))
	for _, event := range events {
		if event != nil {
			eventIDs = append(eventIDs, event.ID)
		}
	}
	if len(eventIDs) == 0 {
		return blocked, pending
	}

	var err error
	blocked, err = store.BatchCheckEventsBlocked(eventIDs)
	if err != nil {
		logging.Infof("Error batch-checking blocked events: %v", err)
		blocked = make(map[string]bool)
	}
	pending, err = store.BatchCheckPendingModeration(eventIDs)
	if err != nil {
		logging.Infof("Error batch-checking pending moderation: %v", err)
		pending = make(map[string]bool)
	}
	return blocked, pending
}

func (policy Policy) FilterBatch(events []*nostr.Event, includeSpam bool, limit int) []*nostr.Event {
	if limit <= 0 || len(events) == 0 {
		return nil
	}
	blocked, pending := LoadModerationStatus(policy.store, events)
	visible := make([]*nostr.Event, 0, min(limit, len(events)))
	for _, event := range events {
		if len(visible) >= limit {
			break
		}
		if policy.CanExpose(event, includeSpam, blocked[event.ID], pending[event.ID]) {
			visible = append(visible, event)
		}
	}
	return visible
}

func (policy Policy) CanExpose(event *nostr.Event, includeSpam, blocked, pending bool) bool {
	if event == nil {
		return false
	}
	if policy.accessControl != nil {
		if err := policy.accessControl.CanReadEvent(event, policy.connPubkey, policy.store); err != nil {
			logging.Debugf("[ACCESS CONTROL] Skipping event %s for pubkey %s: %v", event.ID, policy.connPubkey, err)
			return false
		}
	}
	if !canReadPrivateEventKind(event, policy.connPubkey) {
		return false
	}
	if policy.moderationEnabled && !includeSpam {
		if blocked {
			return false
		}
		if pending && policy.strictModeration && policy.connPubkey != event.PubKey {
			return false
		}
	}
	if policy.mutesEnabled && event.Kind == 1 && event.PubKey != policy.connPubkey {
		content := searchquery.NormalizeText(event.Content)
		for _, muteWord := range policy.muteWords {
			muteWord = searchquery.NormalizeText(muteWord)
			if muteWord != "" && strings.Contains(content, muteWord) {
				return false
			}
		}
	}
	return true
}

func canReadPrivateEventKind(event *nostr.Event, connPubkey string) bool {
	switch event.Kind {
	case 10010:
		return connPubkey != "" && connPubkey == event.PubKey
	case 11888, 19841, 19843:
		referenced := firstTagValue(event, "p")
		return referenced == "" || (connPubkey != "" && connPubkey == referenced)
	case 19842:
		return connPubkey != "" && connPubkey == event.PubKey
	default:
		return true
	}
}

func firstTagValue(event *nostr.Event, name string) string {
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
