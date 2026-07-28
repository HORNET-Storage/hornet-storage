package test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNIP50SearchFunctionality(t *testing.T) {
	basePath := t.TempDir()
	storePath := filepath.Join(basePath, "events")
	statsPath := filepath.Join(basePath, "stats.db")
	store, err := badgerhold.InitStore(storePath, statsPath)
	require.NoError(t, err)
	waitForSearchReady(t, store)

	now := nostr.Timestamp(time.Now().Unix())
	events := []*nostr.Event{
		newSearchEvent(1, 1, now, "The Bitcoin protocol uses a peer to peer network", nostr.Tags{{"topic", "money"}}),
		newSearchEvent(2, 1, now-1, "Nostr clients discover relays", nostr.Tags{{"topic", "social"}}),
		newSearchEvent(3, 1, now-2, "A café publishes Unicode notes", nostr.Tags{{"topic", "social"}}),
		newSearchEvent(4, 7, now-3, "bitcoin reaction", nil),
	}
	for _, event := range events {
		require.NoError(t, store.StoreEvent(event))
	}

	t.Run("prefix terms and structured filters", func(t *testing.T) {
		results, err := store.SearchEvents(nostr.Filter{
			Search:  "bit prot",
			Kinds:   []int{1},
			Authors: []string{events[0].PubKey[:16]},
			Tags:    nostr.TagMap{"topic": []string{"money"}},
		}, 0, 10)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, events[0].ID, results[0].ID)
	})

	t.Run("quoted phrase", func(t *testing.T) {
		results, err := store.QueryEvents(nostr.Filter{Search: `"peer to peer"`})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, events[0].ID, results[0].ID)
	})

	t.Run("unicode normalization", func(t *testing.T) {
		results, err := store.QueryEvents(nostr.Filter{Search: "CAFE\u0301"})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, events[2].ID, results[0].ID)
	})

	t.Run("delete removes indexed document", func(t *testing.T) {
		require.NoError(t, store.DeleteEvent(events[1].ID))
		results, err := store.QueryEvents(nostr.Filter{Search: "nostr"})
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	require.NoError(t, store.Cleanup())
	reopened, err := badgerhold.InitStore(storePath, statsPath)
	require.NoError(t, err)
	defer reopened.Cleanup()
	waitForSearchReady(t, reopened)
	results, err := reopened.QueryEvents(nostr.Filter{Search: "bitcoin"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, events[0].ID, results[0].ID)
}

func TestNIP50SearchRanksExactTermsAheadOfPrefixMatches(t *testing.T) {
	basePath := t.TempDir()
	store, err := badgerhold.InitStore(filepath.Join(basePath, "events"), filepath.Join(basePath, "stats.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Cleanup()) })
	waitForSearchReady(t, store)

	now := nostr.Timestamp(time.Now().Unix())
	prefixOnly := newSearchEvent(20, 1, now+10, "bitcoincash network", nil)
	exact := newSearchEvent(21, 1, now, "bitcoin network", nil)
	require.NoError(t, store.StoreEvent(prefixOnly))
	require.NoError(t, store.StoreEvent(exact))

	results, err := store.SearchEvents(nostr.Filter{Search: "bitcoin"}, 0, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, exact.ID, results[0].ID, "an exact term should rank ahead of a newer prefix-only match")
}

func TestNIP50SearchQueryParser(t *testing.T) {
	parsed := search.ParseSearchQuery(`bitcoin "peer network" include:spam lang:en`)
	assert.Equal(t, `bitcoin "peer network"`, parsed.Text)
	assert.True(t, parsed.IsSpamIncluded())
	assert.Equal(t, "en", parsed.Extensions["lang"])
	assert.Equal(t, "bitcoin", search.ParseSearchQuery(`bitcoin lang:en`).Text)
	assert.True(t, search.MatchesText("Bitcoin has a peer network", parsed.Text))
	assert.False(t, search.MatchesText("Bitcoin has a peer relay network", parsed.Text))
}

func TestNIP50RequestJSONPreservesSearch(t *testing.T) {
	req := map[string]interface{}{
		"subscription_id": "test-sub",
		"filters": []map[string]interface{}{{
			"kinds": []int{1}, "search": "bitcoin include:spam", "limit": 10,
		}},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))
	filters := parsed["filters"].([]interface{})
	filter := filters[0].(map[string]interface{})
	assert.Equal(t, "bitcoin include:spam", filter["search"])
}

func waitForSearchReady(t *testing.T, store *badgerhold.BadgerholdStore) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !store.SearchReady() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, store.SearchReady(), "search index did not become ready")
}

func newSearchEvent(sequence, kind int, createdAt nostr.Timestamp, content string, tags nostr.Tags) *nostr.Event {
	return &nostr.Event{
		ID:        fmt.Sprintf("%064x", sequence),
		PubKey:    fmt.Sprintf("%064x", sequence+100),
		CreatedAt: createdAt,
		Kind:      kind,
		Content:   content,
		Tags:      tags,
	}
}
