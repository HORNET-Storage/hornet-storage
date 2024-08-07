package stores

import (
	"log"

	types "github.com/HORNET-Storage/hornet-storage/lib"
	merkle_dag "github.com/HORNET-Storage/scionic-merkletree/dag"
	"github.com/nbd-wtf/go-nostr"
)

type Store interface {
	InitStore(basepath string, args ...interface{}) error

	// Hornet Storage
	StoreLeaf(root string, leafData *types.DagLeafData) error
	RetrieveLeaf(root string, hash string, includeContent bool) (*types.DagLeafData, error)
	QueryDag(filter map[string]string) ([]string, error)
	StoreDag(dag *types.DagData) error
	BuildDagFromStore(root string, includeContent bool) (*types.DagData, error)
	RetrieveLeafContent(contentHash []byte) ([]byte, error)

	// Nostr
	QueryEvents(filter nostr.Filter) ([]*nostr.Event, error)
	StoreEvent(event *nostr.Event) error
	DeleteEvent(eventID string) error

	// Blossom
	StoreBlob(data []byte, contentType string, publicKey string) (*types.BlobDescriptor, error)
	GetBlob(sha256 string) ([]byte, *string, error)
	DeleteBlob(sha256 string) error
	ListBlobs(pubkey string, since, until int64) ([]types.BlobDescriptor, error)
}

func BuildDagFromStore(store Store, root string, includeContent bool) (*types.DagData, error) {
	builder := merkle_dag.CreateDagBuilder()

	var publicKey *string
	var signature *string
	var addLeavesRecursively func(builder *merkle_dag.DagBuilder, hash string) error

	addLeavesRecursively = func(builder *merkle_dag.DagBuilder, hash string) error {
		data, err := store.RetrieveLeaf(root, hash, includeContent)

		leaf := data.Leaf

		if leaf.Hash == root {
			publicKey = &data.PublicKey
			signature = &data.Signature
		}

		if err != nil {
			log.Println("Unable to find leaf in the database:", err)
			return err
		}

		if !includeContent {
			if leaf.Type == merkle_dag.FileLeafType {
				leaf.Links = make(map[string]string)

				builder.AddLeaf(&leaf, nil)

				return nil
			}
		}

		builder.AddLeaf(&leaf, nil)

		for _, childHash := range leaf.Links {
			if err := addLeavesRecursively(builder, childHash); err != nil {
				log.Println("Error adding child leaf:", err)
			}
		}

		return nil
	}

	if err := addLeavesRecursively(builder, root); err != nil {
		log.Println("Failed to add leaves from database:", err)
		return nil, err
	}

	dag := builder.BuildDag(root)

	data := &types.DagData{
		PublicKey: *publicKey,
		Signature: *signature,
		Dag:       *dag,
	}

	return data, nil
}

func StoreDag(store Store, dag *types.DagData) error {

	return nil
}
