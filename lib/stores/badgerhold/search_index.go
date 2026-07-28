package badgerhold

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/dgraph-io/badger/v4"
	"github.com/fxamacker/cbor/v2"
	"github.com/nbd-wtf/go-nostr"

	"github.com/HORNET-Storage/hornet-storage/lib/config"
	searchquery "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
)

const (
	searchSchemaVersion    = 1
	searchMetadataKey      = "hornets.search.metadata"
	searchMutationPrefix   = "_search:pending:"
	searchMutationUpsert   = "upsert"
	searchMutationDelete   = "delete"
	searchBatchSize        = 256
	defaultSearchPageLimit = 100
)

var ErrSearchUnavailable = errors.New("full-text search index is unavailable")

type searchMetadata struct {
	SchemaVersion int   `json:"schema_version"`
	Kinds         []int `json:"kinds"`
}

type searchMutation struct {
	Operation string `cbor:"o"`
	EventID   string `cbor:"i"`
}

type searchDocument struct {
	Type      string   `json:"_type"`
	ID        string   `json:"id"`
	PubKey    string   `json:"pubkey"`
	Kind      float64  `json:"kind"`
	CreatedAt float64  `json:"created_at"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
}

type bleveSearchIndex struct {
	path              string
	kinds             []int
	mu                sync.RWMutex
	mutationMu        sync.Mutex
	lifecycleMu       sync.Mutex
	index             bleve.Index
	ready             atomic.Bool
	rebuilding        atomic.Bool
	recoveryScheduled atomic.Bool
}

func configuredSearchKinds() []int {
	defaults := []int{1, 31415, 39504}
	cfg, err := config.GetConfig()
	if err != nil || len(cfg.ContentFiltering.TextFilter.FullTextSearchKinds) == 0 {
		return defaults
	}

	kinds := append([]int(nil), cfg.ContentFiltering.TextFilter.FullTextSearchKinds...)
	sort.Ints(kinds)
	unique := kinds[:0]
	for _, kind := range kinds {
		if len(unique) == 0 || unique[len(unique)-1] != kind {
			unique = append(unique, kind)
		}
	}
	return unique
}

func (store *BadgerholdStore) initializeSearchIndex() error {
	kinds := configuredSearchKinds()
	store.searchKinds = make(map[int]struct{}, len(kinds))
	for _, kind := range kinds {
		store.searchKinds[kind] = struct{}{}
	}

	manager := &bleveSearchIndex{
		path:  store.DatabasePath + "-search.bleve",
		kinds: append([]int(nil), kinds...),
	}
	store.searchIndex = manager

	// Initialization shares the lifecycle lock with rebuild and shutdown. A replay
	// failure may schedule recovery, but that recovery cannot race the close below.
	manager.lifecycleMu.Lock()
	index, err := bleve.Open(manager.path)
	if err == nil {
		if metadataErr := validateSearchMetadata(index, manager.kinds); metadataErr == nil {
			manager.index = index
			if replayErr := store.replaySearchMutations(); replayErr == nil {
				manager.ready.Store(true)
				manager.lifecycleMu.Unlock()
				logging.Infof("NIP-50 search index ready at %s", manager.path)
				return nil
			} else {
				logging.Infof("NIP-50 search journal replay failed; rebuilding index: %v", replayErr)
			}
		} else {
			logging.Infof("NIP-50 search index metadata changed; rebuilding index: %v", metadataErr)
		}
		_ = index.Close()
		manager.index = nil
		manager.ready.Store(false)
	} else if !os.IsNotExist(err) {
		logging.Infof("NIP-50 search index could not be opened; rebuilding it: %v", err)
	}
	manager.lifecycleMu.Unlock()

	store.scheduleSearchRecovery()
	return nil
}

func buildSearchMapping() mapping.IndexMapping {
	indexMapping := bleve.NewIndexMapping()
	indexMapping.TypeField = "_type"
	indexMapping.DefaultType = "event"
	indexMapping.IndexDynamic = false
	indexMapping.StoreDynamic = false

	documentMapping := bleve.NewDocumentStaticMapping()
	keyword := func() *mapping.FieldMapping {
		field := bleve.NewKeywordFieldMapping()
		field.Store = false
		return field
	}
	content := bleve.NewTextFieldMapping()
	content.Store = false
	numeric := bleve.NewNumericFieldMapping()
	numeric.Store = false

	documentMapping.AddFieldMappingsAt("id", keyword())
	documentMapping.AddFieldMappingsAt("pubkey", keyword())
	documentMapping.AddFieldMappingsAt("kind", numeric)
	documentMapping.AddFieldMappingsAt("created_at", numeric)
	documentMapping.AddFieldMappingsAt("content", content)
	documentMapping.AddFieldMappingsAt("tags", keyword())
	indexMapping.AddDocumentMapping("event", documentMapping)
	return indexMapping
}

func searchMetadataBytes(kinds []int) ([]byte, error) {
	return json.Marshal(searchMetadata{SchemaVersion: searchSchemaVersion, Kinds: kinds})
}

func validateSearchMetadata(index bleve.Index, kinds []int) error {
	stored, err := index.GetInternal([]byte(searchMetadataKey))
	if err != nil {
		return fmt.Errorf("read search metadata: %w", err)
	}
	expected, err := searchMetadataBytes(kinds)
	if err != nil {
		return err
	}
	if string(stored) != string(expected) {
		return fmt.Errorf("search schema or indexed kinds changed")
	}
	return nil
}

func eventSearchDocument(event *nostr.Event) searchDocument {
	tags := make([]string, 0, len(event.Tags))
	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			tags = append(tags, tag[0]+"\x00"+tag[1])
		}
	}
	return searchDocument{
		Type:      "event",
		ID:        event.ID,
		PubKey:    event.PubKey,
		Kind:      float64(event.Kind),
		CreatedAt: float64(event.CreatedAt),
		Content:   searchquery.NormalizeText(event.Content),
		Tags:      tags,
	}
}

func searchMutationKey(eventID string) []byte {
	return []byte(searchMutationPrefix + eventID)
}

func (store *BadgerholdStore) indexesKind(kind int) bool {
	_, ok := store.searchKinds[kind]
	return ok
}

func (store *BadgerholdStore) queueSearchMutation(tx *badger.Txn, event *nostr.Event, operation string) error {
	if event == nil || !store.indexesKind(event.Kind) {
		return nil
	}
	encoded, err := cbor.Marshal(searchMutation{Operation: operation, EventID: event.ID})
	if err != nil {
		return fmt.Errorf("encode search mutation: %w", err)
	}
	return tx.Set(searchMutationKey(event.ID), encoded)
}

func readSearchMutation(tx *badger.Txn, eventID string) (*searchMutation, error) {
	item, err := tx.Get(searchMutationKey(eventID))
	if err != nil {
		return nil, err
	}
	var mutation searchMutation
	if err := item.Value(func(value []byte) error { return cbor.Unmarshal(value, &mutation) }); err != nil {
		return nil, err
	}
	return &mutation, nil
}

// beginSearchMutation serializes an indexed event's authoritative Badger transaction
// and derived-index update with the rebuild's final swap and journal replay. Without
// this boundary, a Badger commit could land after replay enumerates the journal but
// before readiness is exposed, creating a brief stale-search window.
func (store *BadgerholdStore) beginSearchMutation(event *nostr.Event) func() {
	manager := store.searchIndex
	if manager == nil || event == nil || !store.indexesKind(event.Kind) {
		return func() {}
	}
	manager.mutationMu.Lock()
	return manager.mutationMu.Unlock
}

func (store *BadgerholdStore) applySearchMutationLocked(eventID string, allowUnavailable bool) error {
	var mutation *searchMutation
	var event *nostr.Event
	err := store.Database.Badger().View(func(tx *badger.Txn) error {
		var err error
		mutation, err = readSearchMutation(tx, eventID)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if mutation.Operation == searchMutationUpsert {
			event, err = getEvent(tx, eventID)
			if errors.Is(err, badger.ErrKeyNotFound) {
				mutation.Operation = searchMutationDelete
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil || mutation == nil {
		return err
	}

	switch mutation.Operation {
	case searchMutationUpsert:
		if allowUnavailable {
			err = store.writeSearchDocument(event)
		} else {
			err = store.updateSearchDocument(event)
		}
	case searchMutationDelete:
		if allowUnavailable {
			err = store.removeSearchDocument(eventID)
		} else {
			err = store.deleteSearchDocument(eventID)
		}
	default:
		err = fmt.Errorf("unknown search mutation %q", mutation.Operation)
	}
	if err != nil {
		store.handleSearchFailure(err)
		return err
	}

	err = store.Database.Badger().Update(func(tx *badger.Txn) error {
		return tx.Delete(searchMutationKey(eventID))
	})
	if err != nil {
		return fmt.Errorf("clear search mutation %s: %w", eventID, err)
	}
	return nil
}

func (store *BadgerholdStore) updateSearchDocument(event *nostr.Event) error {
	manager := store.searchIndex
	if manager == nil || !manager.ready.Load() {
		return ErrSearchUnavailable
	}
	return store.writeSearchDocument(event)
}

// writeSearchDocument is reserved for journal replay while queries remain gated by
// SearchReady. Ordinary write paths must call updateSearchDocument instead.
func (store *BadgerholdStore) writeSearchDocument(event *nostr.Event) error {
	manager := store.searchIndex
	if manager == nil {
		return ErrSearchUnavailable
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.index == nil {
		return ErrSearchUnavailable
	}
	return manager.index.Index(event.ID, eventSearchDocument(event))
}

func (store *BadgerholdStore) deleteSearchDocument(eventID string) error {
	manager := store.searchIndex
	if manager == nil || !manager.ready.Load() {
		return ErrSearchUnavailable
	}
	return store.removeSearchDocument(eventID)
}

// removeSearchDocument is the delete counterpart used only during journal replay.
func (store *BadgerholdStore) removeSearchDocument(eventID string) error {
	manager := store.searchIndex
	if manager == nil {
		return ErrSearchUnavailable
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.index == nil {
		return ErrSearchUnavailable
	}
	return manager.index.Delete(eventID)
}

func (store *BadgerholdStore) handleSearchFailure(cause error) {
	manager := store.searchIndex
	if manager == nil || errors.Is(cause, ErrSearchUnavailable) {
		return
	}
	manager.ready.Store(false)
	logging.Infof("NIP-50 search index became unavailable; scheduling rebuild: %v", cause)
	store.scheduleSearchRecovery()
}

func (store *BadgerholdStore) scheduleSearchRecovery() {
	manager := store.searchIndex
	if manager == nil || !manager.recoveryScheduled.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer manager.recoveryScheduled.Store(false)
		backoff := time.Second
		for {
			if store.Ctx.Err() != nil || manager.ready.Load() {
				return
			}

			err := store.RebuildSearchIndex()
			if err == nil {
				return
			}
			if store.Ctx.Err() != nil {
				return
			}
			logging.Infof("NIP-50 search index recovery failed; retrying in %s: %v", backoff, err)

			timer := time.NewTimer(backoff)
			select {
			case <-store.Ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
		}
	}()
}

func (store *BadgerholdStore) SearchReady() bool {
	return store.searchIndex != nil && store.searchIndex.ready.Load()
}

func (store *BadgerholdStore) SearchEvents(filter nostr.Filter, offset, limit int) ([]*nostr.Event, error) {
	manager := store.searchIndex
	if manager == nil || !manager.ready.Load() {
		return nil, ErrSearchUnavailable
	}
	if limit <= 0 {
		limit = defaultSearchPageLimit
	}
	if offset < 0 {
		offset = 0
	}

	searchQuery := buildBleveQuery(filter)
	request := bleve.NewSearchRequestOptions(searchQuery, limit, offset, false)
	request.SortBy([]string{"-_score", "-created_at", "_id"})

	manager.mu.RLock()
	if manager.index == nil {
		manager.mu.RUnlock()
		return nil, ErrSearchUnavailable
	}
	result, err := manager.index.Search(request)
	manager.mu.RUnlock()
	if err != nil {
		store.handleSearchFailure(err)
		return nil, fmt.Errorf("search index query failed: %w", err)
	}

	events := make([]*nostr.Event, 0, len(result.Hits))
	err = store.Database.Badger().View(func(tx *badger.Txn) error {
		for _, hit := range result.Hits {
			event, err := getEvent(tx, hit.ID)
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if searchquery.EventMatchesFilter(event, filter) {
				events = append(events, event)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hydrate search results: %w", err)
	}
	return events, nil
}

func buildBleveQuery(filter nostr.Filter) query.Query {
	clauses := make([]query.Query, 0, 7)
	parsedSearch := searchquery.ParseSearchQuery(filter.Search)
	terms, phrases := searchquery.SplitSearchText(parsedSearch.Text)

	for _, term := range terms {
		exact := bleve.NewTermQuery(term)
		exact.SetField("content")
		exact.SetBoost(4)
		prefix := bleve.NewPrefixQuery(term)
		prefix.SetField("content")
		clauses = append(clauses, bleve.NewDisjunctionQuery(exact, prefix))
	}
	for _, phrase := range phrases {
		phraseQuery := bleve.NewMatchPhraseQuery(phrase)
		phraseQuery.SetField("content")
		phraseQuery.SetBoost(6)
		clauses = append(clauses, phraseQuery)
	}

	if len(filter.IDs) > 0 {
		clauses = append(clauses, keywordPrefixDisjunction("id", filter.IDs))
	}
	if len(filter.Authors) > 0 {
		clauses = append(clauses, keywordPrefixDisjunction("pubkey", filter.Authors))
	}
	if len(filter.Kinds) > 0 {
		kindQueries := make([]query.Query, 0, len(filter.Kinds))
		for _, kind := range filter.Kinds {
			value := float64(kind)
			kindQuery := bleve.NewNumericRangeInclusiveQuery(&value, &value, boolPointer(true), boolPointer(true))
			kindQuery.SetField("kind")
			kindQueries = append(kindQueries, kindQuery)
		}
		clauses = append(clauses, bleve.NewDisjunctionQuery(kindQueries...))
	}
	if filter.Since != nil || filter.Until != nil {
		var minimum, maximum *float64
		if filter.Since != nil {
			value := float64(*filter.Since)
			minimum = &value
		}
		if filter.Until != nil {
			value := float64(*filter.Until)
			maximum = &value
		}
		timeQuery := bleve.NewNumericRangeInclusiveQuery(minimum, maximum, boolPointer(true), boolPointer(true))
		timeQuery.SetField("created_at")
		clauses = append(clauses, timeQuery)
	}
	for tagName, values := range filter.Tags {
		name := strings.TrimPrefix(tagName, "#")
		tagQueries := make([]query.Query, 0, len(values))
		for _, value := range values {
			tagQuery := bleve.NewTermQuery(name + "\x00" + value)
			tagQuery.SetField("tags")
			tagQueries = append(tagQueries, tagQuery)
		}
		if len(tagQueries) > 0 {
			clauses = append(clauses, bleve.NewDisjunctionQuery(tagQueries...))
		}
	}

	if len(clauses) == 0 {
		return bleve.NewMatchAllQuery()
	}
	return bleve.NewConjunctionQuery(clauses...)
}

func keywordPrefixDisjunction(field string, values []string) query.Query {
	queries := make([]query.Query, 0, len(values))
	for _, value := range values {
		prefix := bleve.NewPrefixQuery(value)
		prefix.SetField(field)
		queries = append(queries, prefix)
	}
	return bleve.NewDisjunctionQuery(queries...)
}

func boolPointer(value bool) *bool {
	return &value
}

func (store *BadgerholdStore) replaySearchMutations() error {
	manager := store.searchIndex
	if manager == nil {
		return ErrSearchUnavailable
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()
	return store.replaySearchMutationsLocked()
}

func (store *BadgerholdStore) replaySearchMutationsLocked() error {
	var eventIDs []string
	err := store.Database.Badger().View(func(tx *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		iterator := tx.NewIterator(opts)
		defer iterator.Close()
		prefix := []byte(searchMutationPrefix)
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			eventIDs = append(eventIDs, strings.TrimPrefix(string(iterator.Item().KeyCopy(nil)), searchMutationPrefix))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("enumerate search journal: %w", err)
	}
	for _, eventID := range eventIDs {
		if err := store.applySearchMutationLocked(eventID, true); err != nil {
			return fmt.Errorf("replay search mutation %s: %w", eventID, err)
		}
	}
	return nil
}

func (store *BadgerholdStore) RebuildSearchIndex() error {
	manager := store.searchIndex
	if manager == nil {
		return ErrSearchUnavailable
	}
	if store.Ctx.Err() != nil {
		return fmt.Errorf("rebuild search index: %w", store.Ctx.Err())
	}
	if !manager.rebuilding.CompareAndSwap(false, true) {
		return ErrSearchUnavailable
	}
	defer manager.rebuilding.Store(false)

	// Serialize the entire rebuild/swap against initialization and shutdown. Cleanup
	// cancels the store context before waiting here, so no rebuilt index can be
	// activated after closeSearchIndex has begun.
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	if store.Ctx.Err() != nil {
		return fmt.Errorf("rebuild search index: %w", store.Ctx.Err())
	}

	manager.ready.Store(false)
	temporaryPath := fmt.Sprintf("%s.rebuild-%d", manager.path, time.Now().UnixNano())
	if err := os.RemoveAll(temporaryPath); err != nil {
		return fmt.Errorf("remove stale search rebuild path: %w", err)
	}
	index, err := bleve.New(temporaryPath, buildSearchMapping())
	if err != nil {
		return fmt.Errorf("create search rebuild index: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = index.Close()
		}
		if !manager.ready.Load() {
			_ = os.RemoveAll(temporaryPath)
		}
	}()

	batch := index.NewBatch()
	batchCount := 0
	indexedCount := 0
	err = store.Database.Badger().View(func(tx *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		iterator := tx.NewIterator(opts)
		defer iterator.Close()
		prefix := []byte(prefixEvent)
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			if store.Ctx.Err() != nil {
				return store.Ctx.Err()
			}
			eventID := strings.TrimPrefix(string(iterator.Item().KeyCopy(nil)), prefixEvent)
			event, eventErr := getEvent(tx, eventID)
			if eventErr != nil {
				return eventErr
			}
			if !store.indexesKind(event.Kind) {
				continue
			}
			if eventErr = batch.Index(event.ID, eventSearchDocument(event)); eventErr != nil {
				return eventErr
			}
			batchCount++
			indexedCount++
			if batchCount >= searchBatchSize {
				if eventErr = index.Batch(batch); eventErr != nil {
					return eventErr
				}
				batch = index.NewBatch()
				batchCount = 0
			}
		}
		if batchCount > 0 {
			return index.Batch(batch)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("populate search rebuild index: %w", err)
	}

	metadata, err := searchMetadataBytes(manager.kinds)
	if err != nil {
		return err
	}
	if err := index.SetInternal([]byte(searchMetadataKey), metadata); err != nil {
		return fmt.Errorf("write search metadata: %w", err)
	}
	if err := index.Close(); err != nil {
		return fmt.Errorf("close rebuilt search index: %w", err)
	}
	closed = true

	if store.Ctx.Err() != nil {
		return fmt.Errorf("activate rebuilt search index: %w", store.Ctx.Err())
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()
	if store.Ctx.Err() != nil {
		return fmt.Errorf("activate rebuilt search index: %w", store.Ctx.Err())
	}
	if err := store.activateSearchIndex(temporaryPath); err != nil {
		return err
	}
	if err := store.replaySearchMutationsLocked(); err != nil {
		manager.ready.Store(false)
		return err
	}
	manager.ready.Store(true)
	logging.Infof("NIP-50 search index rebuilt with %d events", indexedCount)
	return nil
}

func (store *BadgerholdStore) activateSearchIndex(temporaryPath string) error {
	manager := store.searchIndex
	backupPath := fmt.Sprintf("%s.previous-%d", manager.path, time.Now().UnixNano())

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.index != nil {
		if err := manager.index.Close(); err != nil {
			return fmt.Errorf("close active search index: %w", err)
		}
		manager.index = nil
	}

	hadExisting := false
	if _, err := os.Stat(manager.path); err == nil {
		if err := os.Rename(manager.path, backupPath); err != nil {
			return fmt.Errorf("backup active search index: %w", err)
		}
		hadExisting = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect active search index: %w", err)
	}
	if err := os.Rename(temporaryPath, manager.path); err != nil {
		if hadExisting {
			_ = os.Rename(backupPath, manager.path)
		}
		return fmt.Errorf("activate rebuilt search index: %w", err)
	}

	index, err := bleve.Open(manager.path)
	if err != nil {
		_ = os.RemoveAll(manager.path)
		if hadExisting {
			_ = os.Rename(backupPath, manager.path)
		}
		return fmt.Errorf("open rebuilt search index: %w", err)
	}
	manager.index = index
	if hadExisting {
		if err := os.RemoveAll(backupPath); err != nil {
			logging.Infof("Unable to remove old NIP-50 search index backup %s: %v", filepath.Base(backupPath), err)
		}
	}
	return nil
}

func (store *BadgerholdStore) closeSearchIndex() error {
	manager := store.searchIndex
	if manager == nil {
		return nil
	}
	manager.ready.Store(false)
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.index == nil {
		return nil
	}
	err := manager.index.Close()
	manager.index = nil
	return err
}
