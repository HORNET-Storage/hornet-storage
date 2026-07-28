package websocket

import (
	"testing"

	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLiveFilterMatchesSearchAndFilterScopedModeration(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("content_filtering.image_moderation.enabled", true)
	viper.Set("event_filtering.moderation_mode", "strict")

	event := &nostr.Event{
		ID:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PubKey:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt: 100,
		Kind:      1,
		Content:   "Repository discovery works over relays",
		Tags:      nostr.Tags{{"topic", "code"}},
	}
	policy := eventvisibility.NewPolicy(nil, "", nil)
	matching := nostr.Filter{
		Search: "repo disc",
		Kinds:  []int{1},
		Tags:   nostr.TagMap{"topic": []string{"code"}},
	}
	require.True(t, liveFilterMatches(event, matching, policy, false, false))

	nonMatching := matching
	nonMatching.Search = "repo missing"
	require.False(t, liveFilterMatches(event, nonMatching, policy, false, false))
	require.False(t, liveFilterMatches(event, matching, policy, true, false), "blocked events should be hidden by default")

	includeSpam := matching
	includeSpam.Search = "repo include:spam"
	require.True(t, liveFilterMatches(event, includeSpam, policy, true, false), "include:spam should apply only to the matching filter")
	require.False(t, liveFilterMatches(event, matching, policy, true, false), "a later filter without include:spam must still hide the event")
}

type readinessStore struct {
	stores.Store
	ready bool
}

func (store readinessStore) SearchReady() bool {
	return store.ready
}

func TestAdvertisedNIPsFollowSearchReadiness(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("relay.supported_nips", []int{1, 50, 65})

	notificationStoreMu.Lock()
	previousStore := notificationStore
	notificationStore = readinessStore{ready: false}
	notificationStoreMu.Unlock()
	t.Cleanup(func() {
		notificationStoreMu.Lock()
		notificationStore = previousStore
		notificationStoreMu.Unlock()
	})

	require.Equal(t, []int{1, 65}, advertisedNIPs())

	notificationStoreMu.Lock()
	notificationStore = readinessStore{ready: true}
	notificationStoreMu.Unlock()
	require.Equal(t, []int{1, 50, 65}, advertisedNIPs())
}
