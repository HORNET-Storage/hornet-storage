package stores

import (
	"fmt"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	merkle_tree "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/tree"
	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	types "github.com/HORNET-Storage/hornet-storage/lib"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/statistics"
	"github.com/ipfs/go-cid"
	"github.com/nbd-wtf/go-nostr"
)

type Store interface {
	Cleanup() error

	// IsClosed returns true if the store has been closed
	IsClosed() bool

	// Statistics Store
	GetStatsStore() statistics.StatisticsStore

	// Hornet Storage
	StoreLeaf(root string, leafData *types.DagLeafData) error
	RetrieveLeaf(root string, hash string, includeContent bool) (*types.DagLeafData, error)
	QueryDag(filter lib_types.QueryFilter) ([]string, error)
	StoreDag(dag *types.DagData) error
	BuildDagFromStore(root string, includeContent bool) (*types.DagData, error)
	BuildPartialDagFromStore(root string, leafHashes []string, includeContent bool, pruneLinks bool) (*types.DagData, error)
	RetrieveContent(rootCID cid.Cid, contentHash []byte) ([]byte, error)
	DeleteDag(root string) error
	CacheLabels(dag *merkle_dag.Dag) error
	RetrieveLabels(root string) (map[string]string, error)
	CreateDagStoreForRoot(root string, publicKey string, signature string) *merkle_dag.DagStore
	CreateDagStoreFromExisting(root string) (*merkle_dag.DagStore, error)
	CacheRelationshipsStreaming(dagStore *merkle_dag.DagStore) error
	CacheLabelsStreaming(dagStore *merkle_dag.DagStore) error
	RetrieveRelationships(root string) (map[string]string, error)

	// Partial DAG support - for verifying referenced leaves exist globally
	HasLeafGlobal(hash string) (bool, error)
	GetLeafLinksGlobal(hash string) ([]string, error)

	// Ownership management
	ClaimOwnership(root string, publicKey string, signature string) error
	GetOwnership(root string) ([]types.DagOwnership, error)
	ReleaseOwnership(root string, publicKey string) error
	HasOwnership(root string) (bool, error)
	FindRootForLeaf(leafHash string) (string, error)

	// Nostr
	QueryEvents(filter nostr.Filter) ([]*nostr.Event, error)
	SearchEvents(filter nostr.Filter, offset, limit int) ([]*nostr.Event, error)
	SearchReady() bool
	StoreEvent(event *nostr.Event) error
	DeleteEvent(eventID string) error
	QueryBlobs(mimeType string) ([]string, error)

	// Moderation
	AddToPendingModeration(eventID string, imageURLs []string) error
	RemoveFromPendingModeration(eventID string) error
	IsPendingModeration(eventID string) (bool, error)
	GetPendingModerationEvents() ([]types.PendingModeration, error)
	GetAndRemovePendingModeration(batchSize int) ([]types.PendingModeration, error)
	MarkEventBlocked(eventID string, timestamp int64) error
	MarkEventBlockedWithDetails(eventID string, timestamp int64, reason string, contentLevel int, mediaURL string) error
	DeleteBlockedEventsOlderThan(age int64) (int, error)
	DeleteResolutionEventsOlderThan(age int64) (int, error)
	IsEventBlocked(eventID string) (bool, error)
	UnmarkEventBlocked(eventID string) error
	BatchCheckEventsBlocked(eventIDs []string) (map[string]bool, error)
	BatchCheckPendingModeration(eventIDs []string) (map[string]bool, error)

	// Dispute Moderation
	AddToPendingDisputeModeration(disputeID string, ticketID string, eventID string, mediaURL string, disputeReason string, userPubKey string) error
	RemoveFromPendingDisputeModeration(disputeID string) error
	IsPendingDisputeModeration(disputeID string) (bool, error)
	GetPendingDisputeModerationEvents() ([]types.PendingDisputeModeration, error)
	GetAndRemovePendingDisputeModeration(batchSize int) ([]types.PendingDisputeModeration, error)
	MarkEventDisputed(eventID string) error
	HasEventDispute(eventID string) (bool, error)
	HasUserDisputedEvent(eventID string, userPubKey string) (bool, error)

	// Pubkey Blocking
	IsBlockedPubkey(pubkey string) (bool, error)
	BlockPubkey(pubkey string, reason string) error
	UnblockPubkey(pubkey string) error
	ListBlockedPubkeys() ([]types.BlockedPubkey, error)

	// Blossom
	StoreBlob(data []byte, hash []byte, publicKey string) error
	GetBlob(hash string) ([]byte, error)
	DeleteBlob(hash string) error

	// Panel
	GetSubscriber(npub string) (*types.Subscriber, error)
	GetSubscriberByAddress(address string) (*types.Subscriber, error)
	SaveSubscriber(subscriber *types.Subscriber) error
	AllocateBitcoinAddress(npub string) (*types.Address, error)
	SaveAddress(addr *types.Address) error
	AllocateAddress() (*types.Address, error)

	// NIP Mapping methods moved to config package
}

func BuildDagFromStore(store Store, root string, includeContent bool) (*types.DagData, error) {
	dag := &merkle_dag.Dag{
		Root:  root,
		Leafs: make(map[string]*merkle_dag.DagLeaf),
	}

	var publicKey *string
	var signature *string
	var addLeavesRecursively func(hash string) error

	addLeavesRecursively = func(hash string) error {
		data, err := store.RetrieveLeaf(root, hash, includeContent)
		if err != nil {
			logging.Infof("Unable to find leaf in the database:%s", err)
			return err
		}

		leaf := data.Leaf

		if leaf.Hash == root {
			publicKey = &data.PublicKey
			signature = &data.Signature
		}

		if !includeContent {
			if leaf.Type == merkle_dag.FileLeafType {
				// Clear links but keep the leaf
				leaf.Links = []string{}
			}
		}

		dag.Leafs[leaf.Hash] = &leaf

		// Recursively add children
		for _, childHash := range leaf.Links {
			// Skip if already added (in case of shared children)
			if _, exists := dag.Leafs[childHash]; !exists {
				if err := addLeavesRecursively(childHash); err != nil {
					logging.Infof("Error adding child leaf:%s", err)
				}
			}
		}

		return nil
	}

	if err := addLeavesRecursively(root); err != nil {
		logging.Infof("Failed to add leaves from database:%s", err)
		return nil, err
	}

	err := dag.Verify()
	if err != nil {
		return nil, err
	}

	data := &types.DagData{
		PublicKey: *publicKey,
		Signature: *signature,
		Dag:       *dag,
	}

	return data, nil
}

// Returns a partial DAG with merkle proofs for verification.
func BuildPartialDagFromStore(store Store, root string, leafHashes []string, includeContent bool, pruneLinks bool) (*types.DagData, error) {
	if len(leafHashes) == 0 {
		return nil, fmt.Errorf("no leaf hashes provided")
	}

	// Load the cached relationships map (childHash -> parentHash)
	relationships, err := store.RetrieveRelationships(root)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve relationships for root %s: %w", root, err)
	}

	// If relationships aren't cached, build and cache them via streaming (one leaf at a time)
	if relationships == nil {
		dagStore, err := store.CreateDagStoreFromExisting(root)
		if err != nil {
			return nil, fmt.Errorf("failed to create dag store for root %s: %w", root, err)
		}

		// CacheRelationshipsStreaming builds index and caches relationships
		if err := store.CacheRelationshipsStreaming(dagStore); err != nil {
			return nil, fmt.Errorf("failed to cache relationships for root %s: %w", root, err)
		}

		// Now retrieve the freshly cached relationships
		relationships, err = store.RetrieveRelationships(root)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve relationships after caching for root %s: %w", root, err)
		}
	}

	partialDag := &merkle_dag.Dag{
		Leafs: make(map[string]*merkle_dag.DagLeaf),
		Root:  root,
	}

	var publicKey *string
	var signature *string

	// Track hashes that are relevant for verification
	relevantHashes := make(map[string]bool)
	relevantHashes[root] = true

	// Track which leaves we've already retrieved to avoid duplicates
	retrievedLeaves := make(map[string]*types.DagLeafData)

	// Process each requested leaf hash
	for _, requestedHash := range leafHashes {
		// Retrieve the target leaf (without content initially)
		targetLeafData, err := store.RetrieveLeaf(root, requestedHash, false)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve leaf %s: %w", requestedHash, err)
		}

		retrievedLeaves[requestedHash] = targetLeafData
		relevantHashes[requestedHash] = true

		// If this is the root, capture public key and signature
		if requestedHash == root {
			publicKey = &targetLeafData.PublicKey
			signature = &targetLeafData.Signature
		}

		// Walk up to root using cached relationships map, building verification path
		currentHash := requestedHash

		for currentHash != root {
			parentHash, exists := relationships[currentHash]
			if !exists || parentHash == "" {
				return nil, fmt.Errorf("leaf %s has no parent in relationships map (broken path to root)", currentHash)
			}

			// Retrieve parent if we haven't already
			if _, exists := retrievedLeaves[parentHash]; !exists {
				parentLeafData, err := store.RetrieveLeaf(root, parentHash, false)
				if err != nil {
					return nil, fmt.Errorf("failed to retrieve parent %s: %w", parentHash, err)
				}
				retrievedLeaves[parentHash] = parentLeafData

				// Capture root's public key and signature
				if parentHash == root {
					publicKey = &parentLeafData.PublicKey
					signature = &parentLeafData.Signature
				}
			}

			relevantHashes[parentHash] = true

			// Move up the tree
			currentHash = parentHash
		}
	}

	// Now we have all leaves in the verification paths
	// Build merkle proofs for parents with multiple children
	for hash, leafData := range retrievedLeaves {
		leaf := &leafData.Leaf

		// Generate proofs for children if this leaf has multiple children
		if leaf.CurrentLinkCount > 1 && len(leaf.Links) > 0 {
			// Build merkle tree from links (which contain all child hashes)
			builder := merkle_tree.CreateTree()
			for _, linkHash := range leaf.Links {
				builder.AddLeaf(linkHash, linkHash)
			}

			merkleTree, _, err := builder.Build()
			if err != nil {
				return nil, fmt.Errorf("failed to build merkle tree for leaf %s: %w", hash, err)
			}

			// Generate proofs for any children that are in our retrieved set
			if leaf.Proofs == nil {
				leaf.Proofs = make(map[string]*merkle_dag.ClassicTreeBranch)
			}

			for _, childHash := range leaf.Links {
				// Only generate proof if this child is in our verification path
				if _, inPath := retrievedLeaves[childHash]; inPath {
					index, exists := merkleTree.GetIndexForKey(childHash)
					if !exists {
						return nil, fmt.Errorf("unable to find index for child %s in parent %s", childHash, hash)
					}

					proof := &merkle_dag.ClassicTreeBranch{
						Leaf:  childHash,
						Proof: merkleTree.Proofs[index],
					}

					leaf.Proofs[childHash] = proof
				}
			}
		}
	}

	// Now handle content retrieval if requested
	if includeContent {
		for hash, leafData := range retrievedLeaves {
			leaf := &leafData.Leaf

			// If leaf has content hash, retrieve the content
			if leaf.ContentHash != nil {
				rootCID, err := cid.Decode(root)
				if err != nil {
					return nil, fmt.Errorf("invalid root CID: %w", err)
				}

				content, err := store.RetrieveContent(rootCID, leaf.ContentHash)
				if err != nil {
					return nil, fmt.Errorf("failed to retrieve content for leaf %s: %w", hash, err)
				}
				leaf.Content = content
			}

			// If leaf has links and is a file type, it might be chunked
			// Retrieve chunk children and their content
			if leaf.Type == merkle_dag.FileLeafType && len(leaf.Links) > 0 {
				for _, chunkHash := range leaf.Links {
					// Skip if we already retrieved this chunk
					if _, exists := retrievedLeaves[chunkHash]; exists {
						continue
					}

					// Retrieve chunk leaf with content
					chunkLeafData, err := store.RetrieveLeaf(root, chunkHash, false)
					if err != nil {
						return nil, fmt.Errorf("failed to retrieve chunk %s: %w", chunkHash, err)
					}

					// Retrieve chunk content
					if chunkLeafData.Leaf.ContentHash != nil {
						rootCID, err := cid.Decode(root)
						if err != nil {
							return nil, fmt.Errorf("invalid root CID: %w", err)
						}

						content, err := store.RetrieveContent(rootCID, chunkLeafData.Leaf.ContentHash)
						if err != nil {
							return nil, fmt.Errorf("failed to retrieve content for chunk %s: %w", chunkHash, err)
						}
						chunkLeafData.Leaf.Content = content
					}

					retrievedLeaves[chunkHash] = chunkLeafData
					relevantHashes[chunkHash] = true
				}
			}
		}
	}

	// Add all retrieved leaves to the partial DAG
	for hash, leafData := range retrievedLeaves {
		partialDag.Leafs[hash] = &leafData.Leaf
	}

	// Optionally prune irrelevant links
	if pruneLinks {
		for _, leaf := range partialDag.Leafs {
			prunedLinks := make([]string, 0, len(leaf.Links))
			for _, linkHash := range leaf.Links {
				if relevantHashes[linkHash] {
					prunedLinks = append(prunedLinks, linkHash)
				}
			}
			leaf.Links = prunedLinks
		}
	}

	// Verify public key and signature are set
	if publicKey == nil || signature == nil {
		return nil, fmt.Errorf("failed to retrieve root leaf for public key and signature")
	}

	data := &types.DagData{
		PublicKey: *publicKey,
		Signature: *signature,
		Dag:       *partialDag,
	}

	return data, nil
}

func StoreDag(store Store, dag *types.DagData) error {
	err := dag.Dag.IterateDag(func(leaf *merkle_dag.DagLeaf, parent *merkle_dag.DagLeaf) error {
		err := store.StoreLeaf(dag.Dag.Root, &types.DagLeafData{
			PublicKey: dag.PublicKey,
			Signature: dag.Signature,
			Leaf:      *leaf,
		})
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	err = store.CacheLabels(&dag.Dag)
	if err != nil {
		return err
	}

	return nil
}
