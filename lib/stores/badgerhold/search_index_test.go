package badgerhold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/dgraph-io/badger/v4"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

func TestSearchJournalReplaysAfterIndexUnavailable(t *testing.T) {
	store := newSearchTestStore(t)
	waitForSearchIndex(t, store)

	store.searchIndex.ready.Store(false)
	event := searchTestEvent(1, "durable journal entry")
	require.NoError(t, store.StoreEvent(event))

	require.NoError(t, store.Database.Badger().View(func(tx *badger.Txn) error {
		mutation, err := readSearchMutation(tx, event.ID)
		require.NoError(t, err)
		require.Equal(t, searchMutationUpsert, mutation.Operation)
		return nil
	}))

	require.NoError(t, store.RebuildSearchIndex())
	waitForSearchIndex(t, store)
	results, err := store.QueryEvents(nostr.Filter{Search: "durable journal"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, event.ID, results[0].ID)

	err = store.Database.Badger().View(func(tx *badger.Txn) error {
		_, err := readSearchMutation(tx, event.ID)
		return err
	})
	require.True(t, errors.Is(err, badger.ErrKeyNotFound), "journal entry should be cleared after replay")
}

func TestSearchDocumentUpsertReplacesIndexedContent(t *testing.T) {
	store := newSearchTestStore(t)
	waitForSearchIndex(t, store)

	event := searchTestEvent(2, "obsolete search phrase")
	require.NoError(t, store.StoreEvent(event))
	results, err := store.QueryEvents(nostr.Filter{Search: "obsolete"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	replacement := *event
	replacement.Content = "replacement search phrase"
	require.NoError(t, store.StoreEvent(&replacement))
	results, err = store.QueryEvents(nostr.Filter{Search: "obsolete"})
	require.NoError(t, err)
	require.Empty(t, results)
	results, err = store.QueryEvents(nostr.Filter{Search: "replacement"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, event.ID, results[0].ID)
}

func TestSearchIndexRecoversOnStartup(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, indexPath string)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, indexPath string) {
				require.NoError(t, os.RemoveAll(indexPath))
			},
		},
		{
			name: "incompatible metadata",
			mutate: func(t *testing.T, indexPath string) {
				index, err := bleve.Open(indexPath)
				require.NoError(t, err)
				require.NoError(t, index.SetInternal([]byte(searchMetadataKey), []byte(`{"schema_version":0,"kinds":[]}`)))
				require.NoError(t, index.Close())
			},
		},
		{
			name: "corrupt",
			mutate: func(t *testing.T, indexPath string) {
				require.NoError(t, os.RemoveAll(indexPath))
				require.NoError(t, os.MkdirAll(indexPath, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(indexPath, "corrupt"), []byte("not a Bleve index"), 0o600))
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			basePath := t.TempDir()
			storePath := filepath.Join(basePath, "events")
			statsPath := filepath.Join(basePath, "stats.db")
			store, err := InitStore(storePath, statsPath)
			require.NoError(t, err)
			waitForSearchIndex(t, store)
			event := searchTestEvent(10, "recoverable index content")
			require.NoError(t, store.StoreEvent(event))
			require.NoError(t, store.Cleanup())

			testCase.mutate(t, storePath+"-search.bleve")
			reopened, err := InitStore(storePath, statsPath)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Cleanup()) })
			waitForSearchIndex(t, reopened)

			results, err := reopened.QueryEvents(nostr.Filter{Search: "recoverable"})
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, event.ID, results[0].ID)
		})
	}
}

func BenchmarkNIP50Search(b *testing.B) {
	basePath := b.TempDir()
	store, err := InitStore(filepath.Join(basePath, "events"), filepath.Join(basePath, "stats.db"))
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, store.Cleanup()) })
	waitForSearchIndex(b, store)
	for i := 0; i < 2000; i++ {
		content := fmt.Sprintf("ordinary indexed event number %d", i)
		if i%25 == 0 {
			content = fmt.Sprintf("common searchable token number %d", i)
		}
		require.NoError(b, store.StoreEvent(searchTestEvent(1000+i, content)))
	}

	b.Run("no-match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			results, err := store.SearchEvents(nostr.Filter{Search: "definitely-absent-token"}, 0, 10)
			require.NoError(b, err)
			require.Empty(b, results)
		}
	})
	b.Run("common-limit-10", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			results, err := store.SearchEvents(nostr.Filter{Search: "common"}, 0, 10)
			require.NoError(b, err)
			require.Len(b, results, 10)
		}
	})
}

func newSearchTestStore(t *testing.T) *BadgerholdStore {
	t.Helper()
	basePath := t.TempDir()
	store, err := InitStore(filepath.Join(basePath, "events"), filepath.Join(basePath, "stats.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Cleanup()) })
	return store
}

func waitForSearchIndex(t testing.TB, store *BadgerholdStore) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !store.SearchReady() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !store.SearchReady() {
		t.Fatalf("search index did not become ready")
	}
}

func searchTestEvent(sequence int, content string) *nostr.Event {
	return &nostr.Event{
		ID:        fmt.Sprintf("%064x", sequence),
		PubKey:    fmt.Sprintf("%064x", sequence+10000),
		CreatedAt: nostr.Timestamp(time.Now().Unix() + int64(sequence)),
		Kind:      1,
		Content:   content,
	}
}
