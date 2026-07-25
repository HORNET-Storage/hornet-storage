package kind5

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/nbd-wtf/go-nostr"
)

func TestApplyDeletionPreservesSignedTombstone(t *testing.T) {
	tempDir := t.TempDir()
	store, err := badgerhold.InitStore(filepath.Join(tempDir, "store"), filepath.Join(tempDir, "stats.db"))
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	defer store.Cleanup()

	privateKey := nostr.GeneratePrivateKey()
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	source := &nostr.Event{
		PubKey:    publicKey,
		CreatedAt: nostr.Timestamp(time.Now().Unix() - 1),
		Kind:      39506,
		Tags: nostr.Tags{
			{"d", "nosis-org-response-test"},
			{"a", "39504:" + publicKey + ":nosis-organization-test"},
		},
		Content: "",
	}
	if err := source.Sign(privateKey); err != nil {
		t.Fatalf("Sign(source): %v", err)
	}
	if err := store.StoreEvent(source); err != nil {
		t.Fatalf("StoreEvent(source): %v", err)
	}

	tombstone := &nostr.Event{
		PubKey:    publicKey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      5,
		Tags: nostr.Tags{
			{"e", source.ID},
			{"a", "39506:" + publicKey + ":nosis-org-response-test"},
			{"k", "39506"},
		},
		Content: "",
	}
	if err := tombstone.Sign(privateKey); err != nil {
		t.Fatalf("Sign(tombstone): %v", err)
	}

	if err := applyDeletionAndStoreTombstone(store, tombstone); err != nil {
		t.Fatalf("applyDeletionAndStoreTombstone: %v", err)
	}
	sourceEvents, err := store.QueryEvents(nostr.Filter{IDs: []string{source.ID}})
	if err != nil {
		t.Fatalf("QueryEvents(source): %v", err)
	}
	if len(sourceEvents) != 0 {
		t.Fatalf("expected source event to be deleted, got %d events", len(sourceEvents))
	}
	tombstones, err := store.QueryEvents(nostr.Filter{IDs: []string{tombstone.ID}})
	if err != nil {
		t.Fatalf("QueryEvents(tombstone): %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected one stored tombstone, got %d", len(tombstones))
	}
	if tombstones[0].Sig != tombstone.Sig {
		t.Fatal("stored tombstone signature changed")
	}
}
