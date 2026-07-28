package filter

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type denyingAccessControl map[string]struct{}

func (denied denyingAccessControl) CanReadEvent(event *nostr.Event, _ string, _ stores.Store) error {
	if _, exists := denied[event.ID]; exists {
		return fmt.Errorf("denied for test")
	}
	return nil
}

func TestSearchBackfillsAfterACLAndModerationFiltering(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("content_filtering.image_moderation.enabled", true)
	viper.Set("event_filtering.moderation_mode", "strict")
	viper.Set("content_filtering.text_filter.search_page_size", 1)
	viper.Set("content_filtering.text_filter.max_search_candidates", 10)

	basePath := t.TempDir()
	store, err := badgerhold.InitStore(filepath.Join(basePath, "events"), filepath.Join(basePath, "stats.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Cleanup()) })
	waitForFilterSearchReady(t, store)

	now := nostr.Timestamp(time.Now().Unix())
	denied := filterSearchEvent(1, now+3, "needle")
	blocked := filterSearchEvent(2, now+2, "needle")
	visible := filterSearchEvent(3, now+1, "needle")
	for _, event := range []*nostr.Event{denied, blocked, visible} {
		require.NoError(t, store.StoreEvent(event))
	}
	require.NoError(t, store.MarkEventBlocked(blocked.ID, time.Now().Unix()))

	policy := eventvisibility.NewPolicy(store, "viewer", denyingAccessControl{denied.ID: {}})
	results, err := queryVisibleEvents(store, nostr.Filter{Search: "needle", Limit: 1}, policy)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, visible.ID, results[0].ID, "search should keep paging after denied and blocked candidates")

	results, err = queryVisibleEvents(store, nostr.Filter{Search: "needle include:spam", Limit: 1}, policy)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, blocked.ID, results[0].ID, "include:spam should bypass moderation for its own filter only")

	results, err = queryVisibleEvents(store, nostr.Filter{Search: "needle", Limit: 1}, policy)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, visible.ID, results[0].ID, "include:spam must not leak into a later filter")
}

func waitForFilterSearchReady(t *testing.T, store *badgerhold.BadgerholdStore) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !store.SearchReady() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, store.SearchReady(), "search index did not become ready")
}

func filterSearchEvent(sequence int, createdAt nostr.Timestamp, content string) *nostr.Event {
	return &nostr.Event{
		ID:        fmt.Sprintf("%064x", sequence),
		PubKey:    fmt.Sprintf("%064x", sequence+100),
		CreatedAt: createdAt,
		Kind:      1,
		Content:   content,
	}
}
