package graviton

import (
	"fmt"
	"log"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/deroproject/graviton"
	"github.com/fxamacker/cbor/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"

	stores "github.com/HORNET-Storage/hornet-storage/lib/stores"
	merkle_dag "github.com/HORNET-Storage/scionic-merkletree/dag"

	jsoniter "github.com/json-iterator/go"

	types "github.com/HORNET-Storage/hornet-storage/lib"

	nostr_handlers "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
)

func InitGorm() (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open("relay_stats.db"), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// Auto migrate the schema
	err = db.AutoMigrate(&types.Kind{}, &types.Photo{}, &types.Video{}, &types.GitNestr{}, &types.UserProfile{})
	if err != nil {
		return nil, err
	}

	return db, nil
}

type GravitonStore struct {
	Database    *graviton.Store
	GormDB      *gorm.DB
	CacheConfig map[string]string
}

func (store *GravitonStore) InitStore(args ...interface{}) error {
	db, err := graviton.NewDiskStore("gravitondb")
	if err != nil {
		return err
	}

	store.Database = db

	snapshot, err := db.LoadSnapshot(0)
	if err != nil {
		return err
	}

	tree, err := snapshot.GetTree("content")
	if err != nil {
		return err
	}

	_, err = graviton.Commit(tree)
	if err != nil {
		return err
	}

	store.CacheConfig = map[string]string{}
	for _, arg := range args {
		if cacheConfig, ok := arg.(map[string]string); ok {
			store.CacheConfig = cacheConfig
		}
	}

	// Initialize Gorm DB
	store.GormDB, err = InitGorm()
	if err != nil {
		return err
	}

	return nil
}

func (store *GravitonStore) QueryDag(filter map[string]string) ([]string, error) {
	keys := []string{}

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	for bucket, key := range filter {
		if _, ok := store.CacheConfig[bucket]; ok {
			cacheTree, err := snapshot.GetTree(bucket)
			if err == nil {
				if strings.HasPrefix(bucket, "npub") {
					value, err := cacheTree.Get([]byte(bucket))
					if err == nil {
						var cacheData *types.CacheData = &types.CacheData{}

						err = cbor.Unmarshal(value, cacheData)
						if err == nil {
							keys = append(keys, cacheData.Keys...)
						}
					}
				} else {
					value, err := cacheTree.Get([]byte(key))
					if err == nil {
						keys = append(keys, string(value))
					}
				}
			}
		}
	}

	return keys, nil
}

func (store *GravitonStore) StoreLeaf(root string, leafData *types.DagLeafData) error {
	if leafData.Leaf.ContentHash != nil && leafData.Leaf.Content == nil {
		return fmt.Errorf("leaf has content hash but no content")
	}

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return err
	}

	var contentTree *graviton.Tree = nil

	if leafData.Leaf.Content != nil {
		contentTree, err = snapshot.GetTree("content")
		if err != nil {
			return err
		}

		err = contentTree.Put(leafData.Leaf.ContentHash, leafData.Leaf.Content)
		if err != nil {
			return err
		}

		leafData.Leaf.Content = nil
	}

	var rootLeaf *merkle_dag.DagLeaf

	if leafData.Leaf.Hash == root {
		rootLeaf = &leafData.Leaf
	} else {
		_rootLeaf, err := store.RetrieveLeaf(root, root, false)
		if err != nil {
			return err
		}

		rootLeaf = &_rootLeaf.Leaf
	}

	bucket := GetBucket(rootLeaf)

	fmt.Printf("Adding to bucket: %s\n", bucket)

	cborData, err := cbor.Marshal(leafData)
	if err != nil {
		return err
	}

	key := leafData.Leaf.Hash // merkle_dag.GetHash(leaf.Hash)

	log.Printf("Adding key to block database: %s\n", key)

	tree, err := snapshot.GetTree(bucket)
	if err != nil {
		return err
	}

	err = tree.Put([]byte(key), cborData)
	if err != nil {
		return err
	}

	trees := []*graviton.Tree{}

	trees = append(trees, tree)

	if rootLeaf.Hash == leafData.Leaf.Hash {
		indexTree, err := snapshot.GetTree("root_index")
		if err != nil {
			return err
		}

		indexTree.Put([]byte(root), []byte(bucket))
		trees = append(trees, indexTree)

		if strings.HasPrefix(leafData.PublicKey, "npub") {
			_trees, err := store.cacheKey(leafData.PublicKey, bucket, root)
			if err == nil {
				trees = append(trees, _trees...)
			}
		}

		if configKey, ok := store.CacheConfig[bucket]; ok {
			valueOfLeaf := reflect.ValueOf(rootLeaf)
			value := valueOfLeaf.FieldByName(configKey)

			if value.IsValid() && value.Kind() == reflect.String {
				cacheKey := value.String()

				_trees, err := store.cacheKey(bucket, cacheKey, root)
				if err == nil {
					trees = append(trees, _trees...)
				}
			}
		}

		// Store photo or video based on file extension if it's a root leaf
		itemName := rootLeaf.ItemName
		leafCount := rootLeaf.LeafCount
		hash := rootLeaf.Hash

		// Determine kind name (extension)
		kindName := GetKindFromItemName(itemName)

		var relaySettings types.RelaySettings
		if err := viper.UnmarshalKey("relay_settings", &relaySettings); err != nil {
			log.Fatalf("Error unmarshaling relay settings: %v", err)
		}

		if contains(relaySettings.Photos, strings.ToLower(kindName)) {
			photo := types.Photo{
				Hash:      hash,
				LeafCount: leafCount,
				KindName:  kindName,
			}
			store.GormDB.Create(&photo)
		} else if contains(relaySettings.Videos, strings.ToLower(kindName)) {
			video := types.Video{
				Hash:      hash,
				LeafCount: leafCount,
				KindName:  kindName,
			}
			store.GormDB.Create(&video)
		}
	}

	if contentTree != nil {
		trees = append(trees, contentTree)
	}

	_, err = graviton.Commit(trees...)
	if err != nil {
		return err
	}

	return nil
}

func GetKindFromItemName(itemName string) string {
	parts := strings.Split(itemName, ".")
	return parts[len(parts)-1]
}

func (store *GravitonStore) RetrieveLeafContent(contentHash []byte) ([]byte, error) {
	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	contentTree, err := snapshot.GetTree("content")
	if err != nil {
		return nil, err
	}

	bytes, err := contentTree.Get(contentHash)
	if err != nil {
		return nil, err
	}

	if len(bytes) > 0 {
		return bytes, nil
	} else {
		return nil, fmt.Errorf("content not found")
	}
}

func (store *GravitonStore) retrieveBucket(root string) (string, error) {
	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return "", err
	}

	tree, err := snapshot.GetTree("root_index")
	if err != nil {
		return "", err
	}

	bytes, err := tree.Get([]byte(root))
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

func (store *GravitonStore) RetrieveLeaf(root string, hash string, includeContent bool) (*types.DagLeafData, error) {
	key := []byte(hash) // merkle_dag.GetHash(hash)

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	bucket, err := store.retrieveBucket(root)
	if err != nil {
		return nil, err
	}

	tree, err := snapshot.GetTree(bucket)
	if err != nil {
		return nil, err
	}

	log.Printf("Searching for leaf with key: %s\nFrom bucket: %s", key, bucket)
	bytes, err := tree.Get(key)
	if err != nil {
		return nil, err
	}

	var data *types.DagLeafData = &types.DagLeafData{}

	err = cbor.Unmarshal(bytes, data)
	if err != nil {
		return nil, err
	}

	if includeContent && data.Leaf.ContentHash != nil {
		fmt.Println("Fetching  leaf content")

		content, err := store.RetrieveLeafContent(data.Leaf.ContentHash)
		if err != nil {
			return nil, err
		}

		data.Leaf.Content = content
	}

	fmt.Println("Leaf found")

	return data, nil
}

func (store *GravitonStore) BuildDagFromStore(root string, includeContent bool) (*types.DagData, error) {
	return stores.BuildDagFromStore(store, root, includeContent)
}

func (store *GravitonStore) StoreDag(dag *types.DagData) error {
	return stores.StoreDag(store, dag)
}

func (store *GravitonStore) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	log.Println("Processing filter:", filter)

	var events []*nostr.Event

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	for kind := range nostr_handlers.GetHandlers() {
		if strings.HasPrefix(kind, "kind") {
			bucket := strings.ReplaceAll(kind, "/", ":")

			tree, err := snapshot.GetTree(bucket)
			if err == nil {
				c := tree.Cursor()

				for _, v, err := c.First(); err == nil; _, v, err = c.Next() {
					var event nostr.Event
					if err := jsoniter.Unmarshal(v, &event); err != nil {
						continue
					}

					if filter.Matches(&event) {
						events = append(events, &event)
					}
				}
			}
		}
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})

	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	log.Println("Found", len(events), "matching events")

	return events, nil
}

func (store *GravitonStore) StoreEvent(event *nostr.Event) error {
	eventData, err := jsoniter.Marshal(event)
	if err != nil {
		return err
	}

	bucket := fmt.Sprintf("kind:%d", event.Kind)

	trees := []*graviton.Tree{}

	ss, _ := store.Database.LoadSnapshot(0)
	tree, _ := ss.GetTree(bucket)

	trees = append(trees, tree)

	if strings.HasPrefix(event.PubKey, "npub") {
		_trees, err := store.cacheKey(event.PubKey, bucket, event.ID)
		if err == nil {
			trees = append(trees, _trees...)
		}
	}

	if configKey, ok := store.CacheConfig[bucket]; ok {
		valueOfLeaf := reflect.ValueOf(event)
		value := valueOfLeaf.FieldByName(configKey)

		if value.IsValid() && value.Kind() == reflect.String {
			cacheKey := value.String()

			_trees, err := store.cacheKey(bucket, cacheKey, event.ID)
			if err == nil {
				trees = append(trees, _trees...)
			}

		}
	}

	tree.Put([]byte(event.ID), eventData)

	_, err = graviton.Commit(trees...)
	if err != nil {
		return err
	}

	// Store event in Gorm SQLite database
	store.storeInGorm(event)

	return nil
}

func (store *GravitonStore) DeleteEvent(eventID string) error {
	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return err
	}

	event, err := store.QueryEvents(nostr.Filter{IDs: []string{eventID}})
	if err != nil {
		return err
	}

	// event kind number is an integer
	kindInt, _ := strconv.ParseInt(fmt.Sprintf("%d", event[0].Kind), 10, 64)

	bucket := fmt.Sprintf("kind:%d", kindInt)

	tree, err := snapshot.GetTree(bucket)
	if err == nil {
		err := tree.Delete([]byte(eventID))
		if err != nil {
			return err
		} else {
			log.Println("Deleted event", eventID)
		}

	}
	graviton.Commit(tree)

	// Delete event from Gorm SQLite database
	store.GormDB.Delete(&types.Kind{}, "event_id = ?", eventID)

	return nil
}

func (store *GravitonStore) cacheKey(bucket string, key string, root string) ([]*graviton.Tree, error) {
	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	trees := []*graviton.Tree{}

	if strings.HasPrefix(bucket, "npub") {
		userTree, err := snapshot.GetTree(bucket)
		if err == nil {
			value, err := userTree.Get([]byte(key))

			if err == nil && value != nil {
				var cacheData *types.CacheData = &types.CacheData{}

				err = cbor.Unmarshal(value, cacheData)
				if err == nil {
					cacheData.Keys = append(cacheData.Keys, root)
				}

				serializedData, err := cbor.Marshal(cacheData)
				if err == nil {
					fmt.Println("CACHE UPDATED: [" + bucket + "]" + bucket + ": " + root)

					userTree.Put([]byte(bucket), serializedData)
				}
			} else {
				cacheData := &types.CacheData{
					Keys: []string{},
				}

				serializedData, err := cbor.Marshal(cacheData)
				if err == nil {
					fmt.Println("CACHE UPDATED: [" + bucket + "]" + bucket + ": " + root)

					userTree.Put([]byte(bucket), serializedData)
				}
			}

			trees = append(trees, userTree)
		}
	} else if _, ok := store.CacheConfig[bucket]; ok {
		cacheTree, err := snapshot.GetTree(fmt.Sprintf("cache:%s", bucket))
		if err == nil {
			cacheTree.Put([]byte(key), []byte(root))

			fmt.Println("CACHE UPDATED: [" + bucket + "]" + key + ": " + root)

			trees = append(trees, cacheTree)
		}
	}

	return trees, nil
}

func GetBucket(leaf *merkle_dag.DagLeaf) string {
	hkind, ok := leaf.AdditionalData["hkind"]
	if ok {
		if hkind != "1" {
			return fmt.Sprintf("hkind:%s", hkind)
		}
	}

	split := strings.Split(leaf.ItemName, ".")

	if len(split) > 1 {
		return split[1]
	} else {
		if leaf.Type == merkle_dag.DirectoryLeafType {
			return "directory"
		} else {
			return "file"
		}
	}
}

func (store *GravitonStore) CountFileLeavesByType() (map[string]int, error) {
	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	treeNames := []string{"content"} // Adjust based on actual storage details.

	fileTypeCounts := make(map[string]int)

	for _, treeName := range treeNames {
		tree, err := snapshot.GetTree(treeName)
		if err != nil {
			continue // Skip if the tree is not found
		}

		c := tree.Cursor()

		for _, v, err := c.First(); err == nil; _, v, err = c.Next() {
			var leaf *merkle_dag.DagLeaf
			err := cbor.Unmarshal(v, &leaf)
			if err != nil {
				continue // Skip on deserialization error
			}

			if leaf.Type == merkle_dag.FileLeafType { // Assuming FileLeafType is the correct constant
				// Extract file extension dynamically
				splitName := strings.Split(leaf.ItemName, ".")
				if len(splitName) > 1 {
					extension := strings.ToLower(splitName[len(splitName)-1])
					fileTypeCounts[extension]++
				}
			}
		}
	}

	return fileTypeCounts, nil
}

// New SQL Function.
func (store *GravitonStore) storeInGorm(event *nostr.Event) {
	kindStr := fmt.Sprintf("kind%d", event.Kind)

	var relaySettings types.RelaySettings
	if err := viper.UnmarshalKey("relay_settings", &relaySettings); err != nil {
		log.Fatalf("Error unmarshaling relay settings: %v", err)
	}

	if event.Kind == 0 {
		// Handle user profile creation or update
		var contentData map[string]interface{}
		if err := jsoniter.Unmarshal([]byte(event.Content), &contentData); err != nil {
			log.Printf("Error unmarshaling event content: %v", err)
			return
		}

		npubKey := event.PubKey
		lightningAddr := false
		dhtKey := false

		if nip05, ok := contentData["nip05"].(string); ok && nip05 != "" {
			lightningAddr = true
		}

		if dht, ok := contentData["dht-key"].(string); ok && dht != "" {
			dhtKey = true
		}

		err := upsertUserProfile(store.GormDB, npubKey, lightningAddr, dhtKey)
		if err != nil {
			log.Printf("Error upserting user profile: %v", err)
		}
	}

	if contains(relaySettings.Kinds, kindStr) {
		kind := types.Kind{
			KindNumber: event.Kind,
			EventID:    event.ID,
		}
		store.GormDB.Create(&kind)
		return
	}

	// Add cases for photos, videos, and gitNestr
	// Assuming you have some way of identifying photos, videos, and gitNestr events
	fmt.Printf("Unhandled kind: %d\n", event.Kind)
}

func upsertUserProfile(db *gorm.DB, npubKey string, lightningAddr, dhtKey bool) error {
	var userProfile types.UserProfile
	result := db.Where("npub_key = ?", npubKey).First(&userProfile)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			// Create new user profile
			userProfile = types.UserProfile{
				NpubKey:       npubKey,
				LightningAddr: lightningAddr,
				DHTKey:        dhtKey,
			}
			return db.Create(&userProfile).Error
		}
		return result.Error
	}

	// Update existing user profile
	userProfile.LightningAddr = lightningAddr
	userProfile.DHTKey = dhtKey
	return db.Save(&userProfile).Error
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}
