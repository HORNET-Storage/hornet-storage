package badgerhold

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/fxamacker/cbor/v2"
	"github.com/nbd-wtf/go-nostr"

	"github.com/HORNET-Storage/hornet-storage/lib/config"
	searchquery "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
)

// ───────────────────────────────────────────────────────────────────
// Key Schema  (raw BadgerDB – no BadgerHold ORM for the event path)
//
//   evt:{eventID}                                         → CBOR(storedEvent)
//   eti:{kind}:{hexTime16}:{eventID}                      → nil   (kind-time)
//   eai:{pubkey}:{hexTime16}:{eventID}                    → nil   (author-time)
//   ets:{hexTime16}:{eventID}                             → nil   (global time)
//   tag:{tagName}:{tagValue}\x00{hexTime16}:{eventID}     → nil   (tag)
//   _schema:version                                       → CBOR(int)
//
// hexTime16  = fmt.Sprintf("%016x", uint64(createdAt))
//              16-char zero-padded hex ⇒ correct lexicographic sort.
// ───────────────────────────────────────────────────────────────────

const (
	prefixEvent      = "evt:"
	prefixKindTime   = "eti:"
	prefixAuthorTime = "eai:"
	prefixEventTime  = "ets:"
	prefixTag        = "tag:"

	schemaVersionKey     = "_schema:version"
	currentSchemaVersion = 2

	defaultMaxLimit = 500
)

// storedEvent is the CBOR value stored at evt:{id}.
// The event ID lives in the key so it is NOT duplicated here.
type storedEvent struct {
	PubKey    string     `cbor:"p"`
	CreatedAt int64      `cbor:"c"`
	Kind      int        `cbor:"k"`
	Tags      nostr.Tags `cbor:"t"`
	Content   string     `cbor:"n"`
	Sig       string     `cbor:"s"`
}

// ──────── key builders ────────

func eventKey(id string) []byte {
	return []byte(prefixEvent + id)
}

func kindTimeKey(kind int, ts int64, id string) []byte {
	return []byte(fmt.Sprintf("%s%d:%016x:%s", prefixKindTime, kind, uint64(ts), id))
}

func authorTimeKey(pub string, ts int64, id string) []byte {
	return []byte(fmt.Sprintf("%s%s:%016x:%s", prefixAuthorTime, pub, uint64(ts), id))
}

func eventTimeKey(ts int64, id string) []byte {
	return []byte(fmt.Sprintf("%s%016x:%s", prefixEventTime, uint64(ts), id))
}

func tagIndexKey(name, value string, ts int64, id string) []byte {
	// \x00 separates variable-length tagValue from the fixed-length suffix
	return []byte(fmt.Sprintf("%s%s:%s\x00%016x:%s", prefixTag, name, value, uint64(ts), id))
}

// ──────── key parsers ────────

// extractEventIDFromKey returns the last 64 characters of any index key
// (event IDs are always 64-char hex at the tail).
func extractEventIDFromKey(key []byte) string {
	if len(key) < 64 {
		return ""
	}
	return string(key[len(key)-64:])
}

// extractTimestampFromKey returns the embedded timestamp. Layout: …:{16hex}:{64id}
func extractTimestampFromKey(key []byte) int64 {
	if len(key) < 64+1+16 {
		return 0
	}
	hexStr := string(key[len(key)-64-1-16 : len(key)-64-1])
	ts, _ := strconv.ParseUint(hexStr, 16, 64)
	return int64(ts)
}

// ──────── seek helpers (reverse iteration) ────────

// seekEnd returns prefix + 0xFF padding so a reverse iterator starts past all
// matching keys.
func seekEnd(prefix []byte) []byte {
	out := make([]byte, 0, len(prefix)+80)
	out = append(out, prefix...)
	for i := 0; i < 80; i++ {
		out = append(out, 0xFF)
	}
	return out
}

// seekBefore positions a reverse iterator at or before a given timestamp
// within a prefix (for Until bounds).
func seekBefore(prefix []byte, until int64) []byte {
	ts := fmt.Sprintf("%016x:", uint64(until))
	out := make([]byte, 0, len(prefix)+17+64)
	out = append(out, prefix...)
	out = append(out, []byte(ts)...)
	for i := 0; i < 64; i++ {
		out = append(out, 0xFF)
	}
	return out
}

// ──────── low-level helpers ────────

// getEvent fetches and decodes a single event by ID within a read transaction.
func getEvent(tx *badger.Txn, id string) (*nostr.Event, error) {
	item, err := tx.Get(eventKey(id))
	if err != nil {
		return nil, err
	}
	var se storedEvent
	err = item.Value(func(val []byte) error {
		return cbor.Unmarshal(val, &se)
	})
	if err != nil {
		return nil, err
	}
	return &nostr.Event{
		ID:        id,
		PubKey:    se.PubKey,
		CreatedAt: nostr.Timestamp(se.CreatedAt),
		Kind:      se.Kind,
		Tags:      se.Tags,
		Content:   se.Content,
		Sig:       se.Sig,
	}, nil
}

// ──────── StoreEvent ────────

func (store *BadgerholdStore) StoreEvent(ev *nostr.Event) error {
	ts := int64(ev.CreatedAt)

	val, err := cbor.Marshal(storedEvent{
		PubKey:    ev.PubKey,
		CreatedAt: ts,
		Kind:      ev.Kind,
		Tags:      ev.Tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	})
	if err != nil {
		return fmt.Errorf("failed to encode event: %w", err)
	}

	// Serialize the authoritative transaction and derived-index mutation with the
	// rebuild's final journal replay. The Badger transaction remains canonical; if
	// Bleve is unavailable, the durable journal entry is retained for recovery.
	unlockSearchMutation := store.beginSearchMutation(ev)
	err = store.Database.Badger().Update(func(tx *badger.Txn) error {
		if err := tx.Set(eventKey(ev.ID), val); err != nil {
			return err
		}
		if err := tx.Set(kindTimeKey(ev.Kind, ts, ev.ID), nil); err != nil {
			return err
		}
		if err := tx.Set(authorTimeKey(ev.PubKey, ts, ev.ID), nil); err != nil {
			return err
		}
		if err := tx.Set(eventTimeKey(ts, ev.ID), nil); err != nil {
			return err
		}
		for _, tag := range ev.Tags {
			if len(tag) < 2 || tag[0] == "" {
				continue
			}
			if err := tx.Set(tagIndexKey(tag[0], tag[1], ts, ev.ID), nil); err != nil {
				return err
			}
		}
		if err := store.queueSearchMutation(tx, ev, searchMutationUpsert); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		unlockSearchMutation()
		return err
	}
	if err := store.applySearchMutationLocked(ev.ID, false); err != nil && !errors.Is(err, ErrSearchUnavailable) {
		logging.Infof("Failed to update search index for event %s: %v\n", ev.ID, err)
	}
	unlockSearchMutation()

	// ── post-write bookkeeping (non-DB) ──
	eventsStoredCount.Add(1)

	if store.StatsDatabase != nil {
		if err := store.StatsDatabase.SaveEventKind(ev); err != nil {
			logging.Infof("Failed to record event statistics: %v\n", err)
		}
	}

	// Image moderation check
	if cfg, err := config.GetConfig(); err != nil {
		logging.Infof("Failed to get config for image moderation check: %v", err)
	} else if cfg.ContentFiltering.ImageModeration.Enabled {
		imageURLs := ExtractImageURLsFromEvent(ev)
		if len(imageURLs) > 0 {
			if ac := websocket.GetAccessControl(); ac != nil {
				if settings := ac.GetSettings(); settings != nil && strings.ToLower(settings.Mode) == "exclusive" {
					logging.Infof("Event %s contains %d images, but skipping moderation in exclusive mode", ev.ID, len(imageURLs))
				} else {
					logging.Infof("Event %s contains %d images, adding to moderation queue", ev.ID, len(imageURLs))
					if err := store.AddToPendingModeration(ev.ID, imageURLs); err != nil {
						logging.Infof("Failed to add event %s to pending moderation: %v", ev.ID, err)
					}
				}
			} else {
				logging.Infof("Event %s contains %d images, adding to moderation queue (fallback)", ev.ID, len(imageURLs))
				if err := store.AddToPendingModeration(ev.ID, imageURLs); err != nil {
					logging.Infof("Failed to add event %s to pending moderation: %v", ev.ID, err)
				}
			}
		}
	}

	return nil
}

// ──────── DeleteEvent ────────

func (store *BadgerholdStore) DeleteEvent(eventID string) error {
	// Fetch event to learn which index keys to remove
	var ev *nostr.Event
	err := store.Database.Badger().View(func(tx *badger.Txn) error {
		var e error
		ev, e = getEvent(tx, eventID)
		return e
	})
	if err != nil {
		return fmt.Errorf("event not found for deletion: %w", err)
	}

	ts := int64(ev.CreatedAt)

	unlockSearchMutation := store.beginSearchMutation(ev)
	err = store.Database.Badger().Update(func(tx *badger.Txn) error {
		if err := store.queueSearchMutation(tx, ev, searchMutationDelete); err != nil {
			return err
		}
		if err := tx.Delete(eventKey(eventID)); err != nil {
			return err
		}
		// Best-effort index deletes
		_ = tx.Delete(kindTimeKey(ev.Kind, ts, eventID))
		_ = tx.Delete(authorTimeKey(ev.PubKey, ts, eventID))
		_ = tx.Delete(eventTimeKey(ts, eventID))
		for _, tag := range ev.Tags {
			if len(tag) < 2 || tag[0] == "" {
				continue
			}
			_ = tx.Delete(tagIndexKey(tag[0], tag[1], ts, eventID))
		}
		return nil
	})
	if err != nil {
		unlockSearchMutation()
		return fmt.Errorf("failed to delete event and indexes: %w", err)
	}
	if err := store.applySearchMutationLocked(eventID, false); err != nil && !errors.Is(err, ErrSearchUnavailable) {
		logging.Infof("Failed to remove event %s from search index: %v\n", eventID, err)
	}
	unlockSearchMutation()

	eventsDeletedCount.Add(1)

	if store.StatsDatabase != nil {
		if err := store.StatsDatabase.DeleteEventByID(eventID); err != nil {
			logging.Infof("Failed to delete event from statistics: %v\n", err)
		}
	}

	return nil
}

// ──────── QueryEvents ────────

func (store *BadgerholdStore) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	if store.IsClosed() {
		return nil, fmt.Errorf("database is closed")
	}

	limit := filter.Limit
	if filter.Search != "" {
		return store.SearchEvents(filter, 0, limit)
	}
	if limit <= 0 {
		limit = defaultMaxLimit
	}

	logging.Debugf("QueryEvents: kinds=%v authors=%d ids=%d tags=%d limit=%d",
		filter.Kinds, len(filter.Authors), len(filter.IDs), len(filter.Tags), filter.Limit)

	var events []*nostr.Event

	err := store.Database.Badger().View(func(tx *badger.Txn) error {
		var e error
		switch {
		case len(filter.IDs) > 0:
			events, e = queryByIDs(tx, filter, limit)
		case len(filter.Tags) > 0:
			events, e = queryByTags(tx, filter, limit)
		case len(filter.Authors) > 0:
			events, e = queryByAuthors(tx, filter, limit)
		case len(filter.Kinds) > 0:
			events, e = queryByKinds(tx, filter, limit)
		default:
			events, e = queryAllEvents(tx, filter, limit)
		}
		return e
	})
	return events, err
}

// ──── query strategies ────

func queryByIDs(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	var results []*nostr.Event
	for _, id := range filter.IDs {
		if len(results) >= limit {
			break
		}
		ev, err := getEvent(tx, id)
		if err != nil {
			continue
		}
		if matchesFilter(ev, filter) {
			results = append(results, ev)
		}
	}
	return results, nil
}

func queryByTags(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	if hasNonIndexedTagFilters(filter.Tags) {
		return queryByFilterScan(tx, filter, limit)
	}

	var primaryName string
	var primaryValues []string
	for name, values := range filter.Tags {
		primaryName = strings.TrimPrefix(name, "#")
		primaryValues = values
		break
	}

	prefixes := make([][]byte, len(primaryValues))
	for i, v := range primaryValues {
		prefixes[i] = []byte(fmt.Sprintf("%s%s:%s\x00", prefixTag, primaryName, v))
	}
	return collectFromPrefixes(tx, prefixes, filter, limit)
}

func hasNonIndexedTagFilters(tags nostr.TagMap) bool {
	for name := range tags {
		trimmedName := strings.TrimPrefix(name, "#")
		if len(trimmedName) != 1 {
			return true
		}
	}

	return false
}

func queryByFilterScan(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	if len(filter.Kinds) > 0 {
		return queryByKinds(tx, filter, limit)
	}

	if len(filter.Authors) > 0 {
		return queryByAuthors(tx, filter, limit)
	}

	return queryAllEvents(tx, filter, limit)
}

func queryByAuthors(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	prefixes := make([][]byte, len(filter.Authors))
	for i, a := range filter.Authors {
		prefixes[i] = []byte(prefixAuthorTime + a + ":")
	}
	return collectFromPrefixes(tx, prefixes, filter, limit)
}

func queryByKinds(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	prefixes := make([][]byte, len(filter.Kinds))
	for i, k := range filter.Kinds {
		prefixes[i] = []byte(fmt.Sprintf("%s%d:", prefixKindTime, k))
	}
	return collectFromPrefixes(tx, prefixes, filter, limit)
}

func queryAllEvents(tx *badger.Txn, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	return collectFromPrefixes(tx, [][]byte{[]byte(prefixEventTime)}, filter, limit)
}

// ──── core collector ────

// collectFromPrefixes reverse-iterates one or more index prefixes, fetches
// each event, applies the full filter, and returns up to limit results
// newest-first.
func collectFromPrefixes(tx *badger.Txn, prefixes [][]byte, filter nostr.Filter, limit int) ([]*nostr.Event, error) {
	seen := make(map[string]struct{})
	var results []*nostr.Event

	for _, prefix := range prefixes {
		// For a single prefix we can stop as soon as limit is reached.
		// For multiple prefixes we keep collecting (then merge-sort later).
		if len(prefixes) == 1 && len(results) >= limit {
			break
		}

		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // index keys carry no value
		opts.Reverse = true
		opts.Prefix = prefix // required for reverse prefix iteration in BadgerDB

		it := tx.NewIterator(opts)

		var sk []byte
		if filter.Until != nil {
			sk = seekBefore(prefix, int64(*filter.Until))
		} else {
			sk = seekEnd(prefix)
		}

		it.Seek(sk)
		for it.ValidForPrefix(prefix) {
			key := it.Item().KeyCopy(nil)
			ts := extractTimestampFromKey(key)

			// Since bound – everything older can be skipped
			if filter.Since != nil && ts < int64(*filter.Since) {
				break
			}

			eid := extractEventIDFromKey(key)
			if _, dup := seen[eid]; dup {
				it.Next()
				continue
			}
			seen[eid] = struct{}{}

			ev, err := getEvent(tx, eid)
			if err != nil {
				it.Next()
				continue
			}

			if matchesFilter(ev, filter) {
				results = append(results, ev)
				if len(prefixes) == 1 && len(results) >= limit {
					break
				}
			}

			it.Next()
		}
		it.Close()
	}

	// Multiple prefixes may interleave timestamps – re-sort and truncate.
	if len(prefixes) > 1 {
		sortEventsByCreatedAtDesc(results)
		if len(results) > limit {
			results = results[:limit]
		}
	}

	return results, nil
}

// ──────── filter matching ────────

func matchesFilter(ev *nostr.Event, f nostr.Filter) bool {
	return searchquery.EventMatchesFilter(ev, f)
}

// ──────── sort helper ────────

func sortEventsByCreatedAtDesc(events []*nostr.Event) {
	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})
}

// ──────── schema version ────────

// CheckSchemaVersion verifies the database is on the expected schema version.
// If the database is empty it stamps it with the current version.
// If it contains old BadgerHold event keys it returns an error telling the
// operator to run the migration tool.
func CheckSchemaVersion(db *badger.DB) error {
	var version int
	var hasVersion, hasOldKeys bool

	err := db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(schemaVersionKey))
		if err == badger.ErrKeyNotFound {
			// Look for legacy BadgerHold event keys
			opts := badger.DefaultIteratorOptions
			opts.PrefetchValues = false
			it := tx.NewIterator(opts)
			defer it.Close()
			it.Seek([]byte("bh_NostrEvent:"))
			if it.ValidForPrefix([]byte("bh_NostrEvent:")) {
				hasOldKeys = true
			}
			return nil
		}
		if err != nil {
			return err
		}
		hasVersion = true
		return item.Value(func(val []byte) error {
			return cbor.Unmarshal(val, &version)
		})
	})
	if err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}

	if hasVersion {
		if version != currentSchemaVersion {
			return fmt.Errorf(
				"database schema version %d is not supported (expected %d).\n"+
					"Please run:  nosis-tools db migrate --path <database-path>",
				version, currentSchemaVersion)
		}
		return nil
	}

	if hasOldKeys {
		return fmt.Errorf(
			"this database uses the old BadgerHold event format.\n" +
				"The relay now requires schema v2 (raw BadgerDB events).\n" +
				"Please run:  nosis-tools db migrate --path <database-path>\n" +
				"to convert your data before starting the relay")
	}

	// Fresh database – stamp current version
	return db.Update(func(tx *badger.Txn) error {
		val, _ := cbor.Marshal(currentSchemaVersion)
		return tx.Set([]byte(schemaVersionKey), val)
	})
}
