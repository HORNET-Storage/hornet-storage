package download

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gofiber/contrib/websocket"
	"github.com/ipfs/go-cid"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	types "github.com/HORNET-Storage/hornet-storage/lib"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/transfer"
	stores "github.com/HORNET-Storage/hornet-storage/lib/stores"

	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	lib_stream "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr"
	hsListener "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr/hyperswarm"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
)

func AddDownloadHandler(listener *hsListener.HyperswarmListener, store stores.Store, canDownloadDag func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool) {
	listener.SetStreamHandler("/download", BuildDownloadStreamHandler(store, canDownloadDag))
}

func AddDownloadHandlerForWebsockets(store stores.Store, canDownloadDag func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool) func(*websocket.Conn) {
	ctx := context.Background()

	return func(conn *websocket.Conn) {
		wsStream := &types.WebSocketStream{Conn: conn, Ctx: ctx}

		message, err := lib_stream.WaitForDownloadMessage(wsStream)
		if err != nil {
			lib_stream.WriteErrorToStream(wsStream, "Failed to receive download message", err)
			return
		}

		handleDownload(store, wsStream, message, canDownloadDag)
	}
}

func BuildDownloadStreamHandler(store stores.Store, canDownloadDag func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool) hsListener.StreamHandler {
	downloadStreamHandler := func(stream lib_types.Stream) {
		defer stream.Close()

		message, err := lib_stream.WaitForDownloadMessage(stream)
		if err != nil {
			lib_stream.WriteErrorToStream(stream, "Failed to receive download message", err)
			return
		}

		handleDownload(store, stream, message, canDownloadDag)
	}

	return downloadStreamHandler
}

func handleDownload(store stores.Store, stream lib_types.Stream, message *lib_types.DownloadMessage, canDownloadDag func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool) {
	rootData, err := store.RetrieveLeaf(message.Root, message.Root, false)
	if err != nil {
		// Check if this hash exists as a non-root leaf and provide a helpful error
		if parentRoot, findErr := store.FindRootForLeaf(message.Root); findErr == nil && parentRoot != "" {
			lib_stream.WriteErrorToStream(stream, fmt.Sprintf("Hash '%s' is a leaf hash, not a root hash. It belongs to DAG with root: %s", message.Root, parentRoot), nil)
			return
		}
		lib_stream.WriteErrorToStream(stream, "Node does not have root leaf", err)
		return
	}

	// Validate that ownership record exists - this is required for serving DAGs
	if rootData.PublicKey == "" || rootData.Signature == "" {
		lib_stream.WriteErrorToStream(stream, fmt.Sprintf("No ownership record found for root hash '%s'. The DAG may have been stored without proper signing or has been orphaned.", message.Root), nil)
		return
	}

	rootLeaf := rootData.Leaf

	// Log every download request for diagnostics (previously silent — cost the original WOT bug diagnosis)
	requesterID := "anonymous"
	if message.PublicKey != "" {
		requesterID = message.PublicKey
	}
	logging.Infof("[DOWNLOAD] Request for root %s from %s", message.Root, requesterID)

	if canDownloadDag != nil && !canDownloadDag(&rootLeaf, &message.PublicKey, &message.Signature) {
		logging.Infof("[DOWNLOAD] DENIED root %s for %s", message.Root, requesterID)
		lib_stream.WriteErrorToStream(stream, "Not allowed to download this", nil)
		return
	}

	includeContent := true
	if message.Filter != nil {
		includeContent = message.Filter.IncludeContent
	}

	if message.Filter != nil && (message.Filter.LeafRanges != nil || len(message.Filter.LeafHashes) > 0) {
		handlePartialDownload(store, stream, message, includeContent, rootData)
		return
	}

	handleStreamingDownload(store, stream, message, includeContent, rootData)
}

func handleStreamingDownload(store stores.Store, stream lib_types.Stream, message *lib_types.DownloadMessage, includeContent bool, rootData *types.DagLeafData) {
	dagStore, err := store.CreateDagStoreFromExisting(message.Root)
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to create dag store", err)
		return
	}
	if !dagStore.HasIndex() {
		if err := dagStore.BuildIndex(); err != nil {
			lib_stream.WriteErrorToStream(stream, "Failed to build index", err)
			return
		}
	}
	totalLeaves, err := dagStore.CountLeavesStreaming()
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to count leaves", err)
		return
	}

	const batchSize = 10
	totalPackets := (totalLeaves + batchSize - 1) / batchSize
	sender, err := transfer.NewSender(stream.Context(), stream, totalPackets)
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to initialize DAG transfer", err)
		return
	}
	defer sender.Close()
	var batch []*merkle_dag.TransmissionPacket
	packetIndex := 0
	leafIndex := 0
	parentCache := make(map[string]*merkle_dag.DagLeaf)

	err = dagStore.IterateDagWithIndex(func(leafHash string, parentHash string) error {
		leafIndex++
		leaf, err := dagStore.RetrieveLeafWithoutContent(leafHash)
		if err != nil {
			return err
		}
		if leaf == nil {
			return fmt.Errorf("indexed leaf %s is missing", leafHash)
		}
		if includeContent && len(leaf.ContentHash) > 0 {
			rootCID, err := cid.Decode(message.Root)
			if err != nil {
				return err
			}
			content, err := store.RetrieveContent(rootCID, leaf.ContentHash)
			if err != nil {
				return fmt.Errorf("failed to retrieve content for leaf %s: %w", leaf.Hash, err)
			}
			leaf.Content = content
		}

		proofs := make(map[string]*merkle_dag.ClassicTreeBranch)
		if parentHash != "" {
			parent, cached := parentCache[parentHash]
			if !cached {
				parent, err = dagStore.RetrieveLeafWithoutContent(parentHash)
				if err == nil && parent != nil {
					parentCache[parentHash] = parent
				}
			}
			if parent != nil && parent.CurrentLinkCount > 1 {
				branch, err := parent.GetBranch(leaf.Hash)
				if err == nil && branch != nil {
					proofs[leaf.Hash] = branch
				}
			}
		}
		batch = append(batch, &merkle_dag.TransmissionPacket{Leaf: leaf, ParentHash: parentHash, Proofs: proofs})
		if len(batch) >= batchSize || leafIndex == totalLeaves {
			if err := sendBatch(sender, batch, message.Root, rootData.PublicKey, rootData.Signature, packetIndex, totalPackets); err != nil {
				return err
			}
			batch = batch[:0]
			parentCache = make(map[string]*merkle_dag.DagLeaf)
			packetIndex++
		}
		return nil
	})
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to stream DAG leaves", err)
		return
	}
	if _, err := sender.Finish(); err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to finish DAG transfer", err)
	}
}

func sendBatch(sender *transfer.Sender, batch []*merkle_dag.TransmissionPacket, root string, publicKey string, signature string, packetIndex int, totalPackets int) error {
	batchedPacket := &merkle_dag.BatchedTransmissionPacket{
		Leaves:        make([]*merkle_dag.DagLeaf, len(batch)),
		Relationships: make(map[string]string),
		PacketIndex:   packetIndex,
		TotalPackets:  totalPackets,
	}
	for index, packet := range batch {
		batchedPacket.Leaves[index] = packet.Leaf
		batchedPacket.Relationships[packet.Leaf.Hash] = packet.ParentHash
	}
	uploadMessage := lib_types.UploadMessage{
		Root:          root,
		Packet:        *batchedPacket.ToSerializable(),
		IsFinalPacket: packetIndex == totalPackets-1,
	}
	if packetIndex == 0 {
		uploadMessage.PublicKey = publicKey
		uploadMessage.Signature = signature
	}
	return sender.Send(uploadMessage, batchedPacket)
}

func handlePartialDownload(store stores.Store, stream lib_types.Stream, message *lib_types.DownloadMessage, includeContent bool, rootData *types.DagLeafData) {
	var leafHashes []string

	if len(message.Filter.LeafHashes) > 0 {
		leafHashes = message.Filter.LeafHashes
	} else if message.Filter.LeafRanges != nil {
		labels, err := store.RetrieveLabels(message.Root)
		if err != nil {
			lib_stream.WriteErrorToStream(stream, "Failed to retrieve cached labels", err)
			return
		}

		for i := message.Filter.LeafRanges.From; i <= message.Filter.LeafRanges.To; i++ {
			label := strconv.Itoa(i)
			if hash, exists := labels[label]; exists {
				leafHashes = append(leafHashes, hash)
			} else {
				lib_stream.WriteErrorToStream(stream, "Label not found in cached labels", nil)
				return
			}
		}
	}

	dagData, err := store.BuildPartialDagFromStore(message.Root, leafHashes, includeContent, true)
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to build partial dag from store", err)
		return
	}

	sendDagPackets(stream, dagData)
}

func sendDagPackets(stream lib_types.Stream, dagData *types.DagData) {
	sequence := dagData.Dag.GetBatchedLeafSequence()
	sender, err := transfer.NewSender(stream.Context(), stream, len(sequence))
	if err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to initialize DAG transfer", err)
		return
	}
	defer sender.Close()
	for index, packet := range sequence {
		uploadMessage := lib_types.UploadMessage{
			Root:          dagData.Dag.Root,
			Packet:        *packet.ToSerializable(),
			IsFinalPacket: index == len(sequence)-1,
		}
		if index == 0 {
			uploadMessage.PublicKey = dagData.PublicKey
			uploadMessage.Signature = dagData.Signature
		}
		if err := sender.Send(uploadMessage, packet); err != nil {
			lib_stream.WriteErrorToStream(stream, "Failed to send packet", err)
			return
		}
	}
	if _, err := sender.Finish(); err != nil {
		lib_stream.WriteErrorToStream(stream, "Failed to finish DAG transfer", err)
	}
}
