package graviton

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deroproject/graviton"
	"github.com/fxamacker/cbor/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"

	stores "github.com/HORNET-Storage/hornet-storage/lib/stores"
	merkle_dag "github.com/HORNET-Storage/scionic-merkletree/dag"

	jsoniter "github.com/json-iterator/go"

	types "github.com/HORNET-Storage/hornet-storage/lib"
)

type GravitonStore struct {
	Database    *graviton.Store
	CacheConfig map[string]string
}

func (store *GravitonStore) InitStore(basepath string, args ...interface{}) error {
	db, err := graviton.NewDiskStore(basepath)
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

	return nil
}

func (store *GravitonStore) QueryDag(filter map[string]string) ([]string, error) {
	keys := []string{}

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	for bucket, key := range filter {
		if strings.HasPrefix(bucket, "npub1") {
			userTree, err := snapshot.GetTree(bucket)
			if err == nil {
				value, err := userTree.Get([]byte(key))
				if err == nil {
					var cacheData *types.CacheData = &types.CacheData{}
					err = cbor.Unmarshal(value, cacheData)
					if err == nil {
						keys = append(keys, cacheData.Keys...)
					}
				}
			}
		} else {
			if _, ok := store.CacheConfig[bucket]; ok {
				cacheBucket := fmt.Sprintf("cache:%s", bucket)
				cacheTree, err := snapshot.GetTree(cacheBucket)
				if err == nil {
					value, err := cacheTree.Get([]byte(key))
					if err == nil {
						var cacheData *types.CacheData = &types.CacheData{}
						err = cbor.Unmarshal(value, cacheData)
						if err == nil {
							keys = append(keys, cacheData.Keys...)
						}
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

	leafContentSize := len(hex.EncodeToString(leafData.Leaf.Content))

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

	//fmt.Printf("Adding to bucket: %s\n", bucket)

	cborData, err := cbor.Marshal(leafData)
	if err != nil {
		return err
	}

	key := leafData.Leaf.Hash // merkle_dag.GetHash(leaf.Hash)

	//log.Printf("Adding key to block database: %s\n", key)

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
		indexTree, err := snapshot.GetTree("mbl")
		if err != nil {
			return err
		}

		indexTree.Put([]byte(root), []byte(bucket))
		trees = append(trees, indexTree)

		if leafData.PublicKey != "" {
			pubKey := leafData.PublicKey

			if !strings.HasPrefix(leafData.PublicKey, "npub1") {
				pubKey = "npub1" + pubKey
			}

			_trees, err := store.cacheKey(pubKey, bucket, root)
			if err == nil {
				trees = append(trees, _trees...)
			}
		}

		if configKey, ok := store.CacheConfig[bucket]; ok {
			cacheKey, ok := rootLeaf.AdditionalData[configKey]

			if ok {
				_trees, err := store.cacheKey(bucket, cacheKey, root)
				if err == nil {
					trees = append(trees, _trees...)
				}
			} else {
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
		}

		// Store photo or video based on file extension if it's a root leaf
		itemName := rootLeaf.ItemName
		leafCount := rootLeaf.LeafCount
		hash := rootLeaf.Hash

		kindName := GetKindFromItemName(itemName)

		ChunkSize := 2048 * 1024

		var relaySettings types.RelaySettings
		if err := viper.UnmarshalKey("relay_settings", &relaySettings); err != nil {
			log.Fatalf("Error unmarshaling relay settings: %v", err)
		}

		var sizeMB float64
		if leafCount > 0 {
			sizeMB = float64(leafCount*ChunkSize) / (1024 * 1024) // Convert to MB
		} else {
			sizeBytes := leafContentSize
			sizeMB = float64(sizeBytes) / (1024 * 1024) // Convert to MB
		}

		gormDB, err := InitGorm()
		if err != nil {
			return err
		}

		mode := relaySettings.Mode

		// Process file according to the mode (smart or unlimited)
		if mode == "smart" {
			// In smart mode, check if the file type is blocked
			if contains(append(append(relaySettings.Photos, relaySettings.Videos...), relaySettings.Audio...), strings.ToLower(kindName)) {
				return fmt.Errorf("file type not permitted: %s", kindName)
			}

			// Save the file under the correct category if not blocked
			if contains(relaySettings.Photos, strings.ToLower(kindName)) {
				photo := types.Photo{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&photo)
			} else if contains(relaySettings.Videos, strings.ToLower(kindName)) {
				video := types.Video{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&video)
			} else if contains(relaySettings.Audio, strings.ToLower(kindName)) {
				audio := types.Audio{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&audio)
			} else {
				// Save the file under Misc if it doesn't fall under any specific category
				misc := types.Misc{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  itemName,
					Size:      sizeMB,
				}
				gormDB.Create(&misc)
			}
		} else if mode == "unlimited" {
			// In unlimited mode, check if the file type is blocked
			if contains(append(append(relaySettings.Photos, relaySettings.Videos...), relaySettings.Audio...), strings.ToLower(kindName)) {
				return fmt.Errorf("blocked file type: %s", kindName)
			}

			// Save the file under the correct category if not blocked
			if contains(relaySettings.Photos, strings.ToLower(kindName)) {
				photo := types.Photo{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&photo)
			} else if contains(relaySettings.Videos, strings.ToLower(kindName)) {
				video := types.Video{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&video)
			} else if contains(relaySettings.Audio, strings.ToLower(kindName)) {
				audio := types.Audio{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  kindName,
					Size:      sizeMB,
				}
				gormDB.Create(&audio)
			} else {
				// Save the file under Misc if it doesn't fall under any specific category
				misc := types.Misc{
					Hash:      hash,
					LeafCount: leafCount,
					KindName:  itemName,
					Size:      sizeMB,
				}
				gormDB.Create(&misc)
			}
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

	tree, err := snapshot.GetTree("mbl")
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

	//log.Printf("Searching for leaf with key: %s\nFrom bucket: %s", key, bucket)
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
		//fmt.Println("Fetching  leaf content")

		content, err := store.RetrieveLeafContent(data.Leaf.ContentHash)
		if err != nil {
			return nil, err
		}

		data.Leaf.Content = content
	}

	//fmt.Println("Leaf found")

	return data, nil
}

func (store *GravitonStore) BuildDagFromStore(root string, includeContent bool) (*types.DagData, error) {
	return stores.BuildDagFromStore(store, root, includeContent)
}

func (store *GravitonStore) StoreDag(dag *types.DagData) error {
	return stores.StoreDag(store, dag)
}

func (store *GravitonStore) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	//log.Println("Processing filter:", filter)
	var events []*nostr.Event

	snapshot, err := store.Database.LoadSnapshot(0)
	if err != nil {
		return nil, err
	}

	masterBucketList, err := store.GetMasterBucketList("kinds")
	if err != nil {
		return nil, err
	}

	// Convert search term to lowercase for case-insensitive comparison
	searchTerm := strings.ToLower(filter.Search)

	for _, bucket := range masterBucketList {
		if strings.HasPrefix(bucket, "kind") {
			tree, err := snapshot.GetTree(bucket)
			if err != nil {
				continue // Skip this bucket if there's an error
			}

			c := tree.Cursor()
			for _, v, err := c.First(); err == nil; _, v, err = c.Next() {
				var event nostr.Event
				if err := jsoniter.Unmarshal(v, &event); err != nil {
					continue // Skip on unmarshal error
				}

				// Step 1: Check if the event matches the filter criteria, including kind
				if !filter.Matches(&event) {
					continue
				}

				// Step 2: Implement search logic here, after ensuring the event matches the filter
				if searchTerm != "" && !strings.Contains(strings.ToLower(event.Content), searchTerm) {
					continue // If the lowercase content doesn't contain the lowercase search term, skip
				}

				// If the event passes both the filter and search, add it to the results
				events = append(events, &event)
			}
		}
	}

	// Sort the events based on creation time, most recent first
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt > events[j].CreatedAt
		} else {
			return events[i].ID > events[j].ID
		}
	})

	// Step 3: Apply the limit, if specified
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}

	log.Println("Found", len(events), "matching events")
	return events, nil
}

// func (store *GravitonStore) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
// 	//log.Println("Processing filter:", filter)

// 	var events []*nostr.Event

// 	snapshot, err := store.Database.LoadSnapshot(0)
// 	if err != nil {
// 		return nil, err
// 	}

// 	masterBucketList, err := store.GetMasterBucketList("kinds")
// 	if err != nil {
// 		return nil, err
// 	}

// 	for _, bucket := range masterBucketList {
// 		if strings.HasPrefix(bucket, "kind") {
// 			tree, err := snapshot.GetTree(bucket)
// 			if err == nil {
// 				c := tree.Cursor()

// 				for _, v, err := c.First(); err == nil; _, v, err = c.Next() {
// 					var event nostr.Event
// 					if err := jsoniter.Unmarshal(v, &event); err != nil {
// 						continue
// 					}

// 					if filter.Matches(&event) {
// 						events = append(events, &event)
// 					}
// 				}
// 			}
// 		}
// 	}

// 	sort.Slice(events, func(i, j int) bool {
// 		return events[i].CreatedAt > events[j].CreatedAt
// 	})

// 	if filter.Limit > 0 && len(events) > filter.Limit {
// 		events = events[:filter.Limit]
// 	}

// 	//log.Println("Found", len(events), "matching events")

// 	return events, nil
// }

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

	err = tree.Put([]byte(event.ID), eventData)
	if err != nil {
		return err
	}

	masterBucketListTree, err := store.UpdateMasterBucketList("kinds", bucket)
	if err != nil {
		return err
	}

	if masterBucketListTree != nil {
		trees = append(trees, masterBucketListTree)
	}

	_, err = graviton.Commit(trees...)
	if err != nil {
		return err
	}

	// Store event in Gorm SQLite database
	storeInGorm(event)

	return nil
}

func (store *GravitonStore) UpdateMasterBucketList(key string, bucket string) (*graviton.Tree, error) {
	snapshot, _ := store.Database.LoadSnapshot(0)

	tree, err := snapshot.GetTree("mbl")
	if err != nil {
		return nil, err
	}

	var masterBucketList []string

	bytes, err := tree.Get([]byte(fmt.Sprintf("mbl_%s", key)))
	if bytes == nil || err != nil {
		masterBucketList = []string{}
	} else {
		err = cbor.Unmarshal(bytes, &masterBucketList)
		if err != nil {
			return nil, err
		}
	}

	if contains(masterBucketList, bucket) {
		return nil, nil
	} else {
		masterBucketList = append(masterBucketList, bucket)

		bytes, err = cbor.Marshal(masterBucketList)
		if err != nil {
			return nil, err
		}

		err = tree.Put([]byte(fmt.Sprintf("mbl_%s", key)), bytes)
		if err != nil {
			return nil, err
		}
	}

	return tree, nil
}

func (store *GravitonStore) GetMasterBucketList(key string) ([]string, error) {
	snapshot, _ := store.Database.LoadSnapshot(0)

	tree, err := snapshot.GetTree("mbl")
	if err != nil {
		return nil, err
	}

	var masterBucketList []string

	bytes, err := tree.Get([]byte(fmt.Sprintf("mbl_%s", key)))
	if bytes == nil || err != nil {
		masterBucketList = []string{}
	} else {
		err = cbor.Unmarshal(bytes, &masterBucketList)
		if err != nil {
			return nil, err
		}
	}

	return masterBucketList, nil
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

	gormDB, err := InitGorm()
	if err != nil {
		return err
	}

	// Delete event from Gorm SQLite database
	gormDB.Delete(&types.Kind{}, "event_id = ?", eventID)

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
				if err == nil && !contains(cacheData.Keys, root) {
					cacheData.Keys = append(cacheData.Keys, root)

					serializedData, err := cbor.Marshal(cacheData)
					if err == nil {
						userTree.Put([]byte(key), serializedData)

						trees = append(trees, userTree)
					}
				}
			} else {
				cacheData := &types.CacheData{
					Keys: []string{root},
				}

				serializedData, err := cbor.Marshal(cacheData)
				if err == nil {
					userTree.Put([]byte(key), serializedData)

					trees = append(trees, userTree)
				}
			}

		}

		/*
			masterBucketListTree, err := store.UpdateMasterBucketList("npubs", bucket)
			if err != nil {
				return trees, nil
			}

			if masterBucketListTree != nil {
				trees = append(trees, masterBucketListTree)
			}
		*/
	} else if _, ok := store.CacheConfig[bucket]; ok {
		cacheBucket := fmt.Sprintf("cache:%s", bucket)

		cacheTree, err := snapshot.GetTree(cacheBucket)
		if err == nil {
			value, err := cacheTree.Get([]byte(key))

			if err == nil && value != nil {
				var cacheData *types.CacheData = &types.CacheData{}

				err = cbor.Unmarshal(value, cacheData)
				if err == nil && !contains(cacheData.Keys, root) {
					cacheData.Keys = append(cacheData.Keys, root)

					serializedData, err := cbor.Marshal(cacheData)
					if err == nil {
						cacheTree.Put([]byte(key), serializedData)

						trees = append(trees, cacheTree)
					}
				}
			} else {
				cacheData := &types.CacheData{
					Keys: []string{root},
				}

				serializedData, err := cbor.Marshal(cacheData)
				if err == nil {
					cacheTree.Put([]byte(key), serializedData)

					trees = append(trees, cacheTree)
				}
			}
		}

		/*
			masterBucketListTree, err := store.UpdateMasterBucketList("scionic", bucket)
			if err != nil {
				return trees, nil
			}

			if masterBucketListTree != nil {
				trees = append(trees, masterBucketListTree)
			}
		*/
	}

	return trees, nil
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

func (store *GravitonStore) StoreBlob(data []byte, contentType string, publicKey string) (*types.BlobDescriptor, error) {
	snapshot, _ := store.Database.LoadSnapshot(0)
	blossomTree, _ := snapshot.GetTree("blossom")
	contentTree, _ := snapshot.GetTree("content")

	hash := sha256.Sum256(data)
	encodedHash := hex.EncodeToString(hash[:])

	descriptor := types.BlobDescriptor{
		URL:      fmt.Sprintf("/%s", encodedHash),
		SHA256:   encodedHash,
		Size:     int64(len(data)),
		Type:     contentType,
		Uploaded: time.Now().Unix(),
	}

	cacheTrees, err := store.cacheKey(publicKey, "blossom", encodedHash)
	if err != nil {
		return nil, err
	}

	serializedDescriptor, err := cbor.Marshal(descriptor)
	if err != nil {
		return nil, err
	}

	blossomTree.Put(hash[:], serializedDescriptor)
	contentTree.Put(hash[:], data)

	cacheTrees = append(cacheTrees, blossomTree)
	cacheTrees = append(cacheTrees, contentTree)

	graviton.Commit(cacheTrees...)

	return &descriptor, nil
}

func (store *GravitonStore) GetBlob(hash string) ([]byte, *string, error) {
	snapshot, _ := store.Database.LoadSnapshot(0)
	blossomTree, _ := snapshot.GetTree("blossom")
	contentTree, _ := snapshot.GetTree("content")

	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		return nil, nil, err
	}

	serializedDescriptor, err := blossomTree.Get(hashBytes)
	if err != nil {
		return nil, nil, err
	}

	var descriptor types.BlobDescriptor
	err = cbor.Unmarshal(serializedDescriptor, &descriptor)
	if err != nil {
		return nil, nil, err
	}

	content, err := contentTree.Get(hashBytes)
	if err != nil {
		return nil, nil, err
	}

	return content, &descriptor.Type, nil
}

func (store *GravitonStore) DeleteBlob(hash string) error {
	snapshot, _ := store.Database.LoadSnapshot(0)
	blossomTree, _ := snapshot.GetTree("blossom")
	contentTree, _ := snapshot.GetTree("content")

	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		return err
	}

	blossomTree.Delete(hashBytes)
	contentTree.Delete(hashBytes)

	graviton.Commit(blossomTree, contentTree)

	return nil
}

func (store *GravitonStore) ListBlobs(pubkey string, since, until int64) ([]types.BlobDescriptor, error) {
	snapshot, _ := store.Database.LoadSnapshot(0)
	blossomTree, _ := snapshot.GetTree("blossom")
	cursor := blossomTree.Cursor()

	results := []types.BlobDescriptor{}

	userTree, err := snapshot.GetTree(pubkey)
	if err == nil {
		value, err := userTree.Get([]byte("blossom"))

		if err == nil && value != nil {
			var cacheData *types.CacheData = &types.CacheData{}

			err = cbor.Unmarshal(value, cacheData)
			if err == nil {
				for _, key := range cacheData.Keys {
					hashBytes, err := hex.DecodeString(key)
					if err != nil {
						return nil, err
					}

					serializedDescriptor, err := blossomTree.Get(hashBytes)
					if err != nil {
						return nil, err
					}

					var descriptor types.BlobDescriptor
					err = cbor.Unmarshal(serializedDescriptor, &descriptor)
					if err != nil {
						return nil, err
					}

					if descriptor.Uploaded >= since && descriptor.Uploaded <= until {
						results = append(results, descriptor)
					}
				}
			}
		}
	}

	_, value, cursorErr := cursor.First()
	for {
		if cursorErr == nil {
			break
		}

		var descriptor types.BlobDescriptor
		err := cbor.Unmarshal(value, &descriptor)
		if err != nil {
			return nil, err
		}

		if descriptor.Uploaded >= since && descriptor.Uploaded <= until {
			results = append(results, descriptor)
		}

		_, value, cursorErr = cursor.Next()
	}

	return results, nil
}

func GetBucket(leaf *merkle_dag.DagLeaf) string {
	app, ok := leaf.AdditionalData["path"]
	if ok {
		return app
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

//type RootInfo struct {
//	Hash      string
//	Timestamp uint64
//}
//
//func (store *GravitonStore) GetRoots(rootHashes []string) ([]RootInfo, error) {
//	roots := []RootInfo{}
//
//	snapshot, err := store.Database.LoadSnapshot(0)
//	if err != nil {
//		return nil, fmt.Errorf("failed to load snapshot: %v", err)
//	}
//
//	indexTree, err := snapshot.GetTree("mbl")
//	if err != nil {
//		return nil, fmt.Errorf("failed to get index tree: %v", err)
//	}
//
//	cursor := indexTree.Cursor()
//	for k, v, err := cursor.First(); err == nil; k, v, err = cursor.Next() {
//		if slices.Contains(rootHashes, string(k)) || len(rootHashes) == 0 {
//			// TODO: WE NEED TO HAVE A TIMESTAMP HERE
//			var timestamp uint64
//			if err := cbor.Unmarshal(v, &timestamp); err != nil {
//				return nil, fmt.Errorf("failed to unmarshal timestamp for root %s: %v", k, err)
//			}
//
//			roots = append(roots, RootInfo{
//				Hash:      string(k),
//				Timestamp: timestamp,
//			})
//		}
//
//	}
//
//	return roots, nil
//}
//
//// PutRoots stores multiple root hashes with their timestamps
//func (store *GravitonStore) PutRoots(roots []RootInfo) error {
//	snapshot, err := store.Database.LoadSnapshot(0)
//	if err != nil {
//		return fmt.Errorf("failed to load snapshot: %v", err)
//	}
//
//	indexTree, err := snapshot.GetTree("mbl")
//	if err != nil {
//		return fmt.Errorf("failed to get index tree: %v", err)
//	}
//
//	for _, root := range roots {
//		// Serialize the RootInfo
//		rootInfoBytes, err := cbor.Marshal(root)
//		if err != nil {
//			return fmt.Errorf("failed to marshal root info for %s: %v", root.Hash, err)
//		}
//
//		// Store the serialized RootInfo in the index tree
//		err = indexTree.Put([]byte(root.Hash), rootInfoBytes)
//		if err != nil {
//			return fmt.Errorf("failed to store root info for %s: %v", root.Hash, err)
//		}
//	}
//
//	// Commit the changes
//	_, err = graviton.Commit(indexTree)
//	if err != nil {
//		return fmt.Errorf("failed to commit changes: %v", err)
//	}
//
//	return nil
//}
