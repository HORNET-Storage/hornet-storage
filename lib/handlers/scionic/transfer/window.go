package transfer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	lib_stream "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr"
)

const (
	Protocol               = "scionic-window/1"
	WindowPackets          = 16
	WindowBytes      int64 = 32 * 1024 * 1024
	maxWindowPackets       = 64
	maxWindowBytes   int64 = 256 * 1024 * 1024
)

type Ack struct {
	Index   int
	Packets int
	Bytes   int64
}

type Stats struct {
	AckWait time.Duration
	Enabled bool
	Packets int
	Bytes   int64
}

func EncodeAck(index int) string {
	return fmt.Sprintf("%s %d %d %d", Protocol, index, WindowPackets, WindowBytes)
}

func ParseAck(message string) (Ack, bool, error) {
	if !strings.HasPrefix(message, "scionic-window/") {
		return Ack{}, false, nil
	}
	parts := strings.Fields(message)
	if len(parts) != 4 || parts[0] != Protocol {
		return Ack{}, true, fmt.Errorf("unsupported or malformed Scionic transfer acknowledgment %q", message)
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 0 {
		return Ack{}, true, fmt.Errorf("invalid cumulative acknowledgment index %q", parts[1])
	}
	packets, err := strconv.Atoi(parts[2])
	if err != nil || packets < 1 || packets > maxWindowPackets {
		return Ack{}, true, fmt.Errorf("invalid transfer window packet limit %q", parts[2])
	}
	bytes, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || bytes < 1 || bytes > maxWindowBytes {
		return Ack{}, true, fmt.Errorf("invalid transfer window byte limit %q", parts[3])
	}
	return Ack{Index: index, Packets: packets, Bytes: bytes}, true, nil
}

func PacketSize(packet *merkle_dag.BatchedTransmissionPacket) int64 {
	if packet == nil {
		return 0
	}
	size := int64(128)
	for _, leaf := range packet.Leaves {
		if leaf != nil {
			size += int64(leaf.EstimateSize()) + 256
		}
	}
	for child, parent := range packet.Relationships {
		size += int64(len(child) + len(parent) + 32)
	}
	return size
}

type ackResult struct {
	response *lib_types.ResponseMessage
	err      error
}

type Sender struct {
	ctx           context.Context
	stream        lib_types.Stream
	total         int
	next          int
	highestAck    int
	window        Ack
	stats         Stats
	negotiated    bool
	inFlightBytes int64
	inFlightSizes map[int]int64
	results       chan ackResult
	done          chan struct{}
	closeOnce     sync.Once
}

func NewSender(ctx context.Context, stream lib_types.Stream, total int) (*Sender, error) {
	if total < 1 {
		return nil, fmt.Errorf("cannot send an empty packet sequence")
	}
	return &Sender{
		ctx:           ctx,
		stream:        stream,
		total:         total,
		highestAck:    -1,
		inFlightSizes: make(map[int]int64),
		done:          make(chan struct{}),
	}, nil
}

func (sender *Sender) Close() {
	sender.closeOnce.Do(func() { close(sender.done) })
}

func (sender *Sender) validateResponse(response *lib_types.ResponseMessage) error {
	if response == nil {
		return fmt.Errorf("peer returned no packet acknowledgment")
	}
	if !response.Ok {
		if response.Message != "" {
			return fmt.Errorf("peer rejected packet: %s", response.Message)
		}
		return fmt.Errorf("peer rejected packet")
	}
	return nil
}

func (sender *Sender) waitResponse() (*lib_types.ResponseMessage, error) {
	started := time.Now()
	response, err := lib_stream.WaitForResponse(sender.stream)
	sender.stats.AckWait += time.Since(started)
	return response, err
}

func (sender *Sender) startAckReader() {
	sender.results = make(chan ackResult, sender.window.Packets+1)
	go func() {
		responseReader := lib_stream.NewMessageReader(sender.stream)
		for remaining := sender.total - 1; remaining > 0; remaining-- {
			response, err := lib_stream.WaitForResponseFromReader(responseReader)
			select {
			case sender.results <- ackResult{response: response, err: err}:
			case <-sender.done:
				return
			}
			if err != nil || response == nil || !response.Ok {
				return
			}
		}
	}()
}

func (sender *Sender) readCumulativeAck() error {
	started := time.Now()
	var result ackResult
	select {
	case result = <-sender.results:
	case <-sender.ctx.Done():
		return sender.ctx.Err()
	case <-sender.stream.Context().Done():
		return sender.stream.Context().Err()
	}
	sender.stats.AckWait += time.Since(started)
	if result.err != nil {
		return fmt.Errorf("failed to receive cumulative acknowledgment: %w", result.err)
	}
	if err := sender.validateResponse(result.response); err != nil {
		return err
	}
	ack, supported, err := ParseAck(result.response.Message)
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("peer removed sliding-window metadata after negotiation")
	}
	if ack.Packets != sender.window.Packets || ack.Bytes != sender.window.Bytes {
		return fmt.Errorf("peer changed negotiated transfer window")
	}
	if ack.Index <= sender.highestAck {
		return fmt.Errorf("duplicate or regressing cumulative acknowledgment %d after %d", ack.Index, sender.highestAck)
	}
	if ack.Index >= sender.next || ack.Index >= sender.total {
		return fmt.Errorf("cumulative acknowledgment %d covers an unsent or out-of-range packet", ack.Index)
	}
	for index := sender.highestAck + 1; index <= ack.Index; index++ {
		size, exists := sender.inFlightSizes[index]
		if !exists {
			return fmt.Errorf("cumulative acknowledgment skipped unknown packet %d", index)
		}
		sender.inFlightBytes -= size
		delete(sender.inFlightSizes, index)
	}
	sender.highestAck = ack.Index
	return nil
}

func (sender *Sender) Send(message lib_types.UploadMessage, packet *merkle_dag.BatchedTransmissionPacket) error {
	index := sender.next
	if index >= sender.total {
		return fmt.Errorf("attempted to send packet after declared total %d", sender.total)
	}
	if packet == nil || packet.PacketIndex != index || packet.TotalPackets != sender.total {
		return fmt.Errorf("invalid transmission counters at packet %d", index)
	}
	if message.Root == "" || message.IsFinalPacket != (index == sender.total-1) {
		return fmt.Errorf("packet %d has invalid root or final marker", index)
	}

	if index == 0 {
		if message.PublicKey == "" || message.Signature == "" {
			return fmt.Errorf("root packet is missing ownership authentication")
		}
		if err := lib_stream.WriteMessageToStream(sender.stream, message); err != nil {
			return err
		}
		sender.next = 1
		response, err := sender.waitResponse()
		if err != nil {
			return err
		}
		if err := sender.validateResponse(response); err != nil {
			return err
		}
		ack, supported, err := ParseAck(response.Message)
		if err != nil {
			return err
		}
		if !supported {
			sender.highestAck = 0
			return nil
		}
		if ack.Index != 0 {
			return fmt.Errorf("root acknowledgment advanced to packet %d instead of 0", ack.Index)
		}
		sender.negotiated = true
		sender.window = ack
		sender.stats.Enabled = true
		sender.stats.Packets = ack.Packets
		sender.stats.Bytes = ack.Bytes
		sender.highestAck = 0
		if sender.total > 1 {
			sender.startAckReader()
		}
		return nil
	}

	if !sender.negotiated {
		if err := lib_stream.WriteMessageToStream(sender.stream, message); err != nil {
			return err
		}
		sender.next++
		response, err := sender.waitResponse()
		if err != nil {
			return err
		}
		if err := sender.validateResponse(response); err != nil {
			return err
		}
		sender.highestAck = index
		return nil
	}

	size := PacketSize(packet)
	for len(sender.inFlightSizes) >= sender.window.Packets || (sender.inFlightBytes+size > sender.window.Bytes && len(sender.inFlightSizes) > 0) {
		if err := sender.readCumulativeAck(); err != nil {
			return err
		}
	}
	if err := lib_stream.WriteMessageToStream(sender.stream, message); err != nil {
		return err
	}
	sender.inFlightSizes[index] = size
	sender.inFlightBytes += size
	sender.next++
	return nil
}

func (sender *Sender) Finish() (Stats, error) {
	defer sender.Close()
	if sender.next != sender.total {
		return sender.stats, fmt.Errorf("packet stream ended after %d of %d packets", sender.next, sender.total)
	}
	if sender.negotiated {
		for sender.highestAck < sender.total-1 {
			if err := sender.readCumulativeAck(); err != nil {
				return sender.stats, err
			}
		}
	}
	return sender.stats, nil
}

type ReceiverState struct {
	root       string
	publicKey  string
	signature  string
	total      int
	next       int
	windowed   bool
	finalized  bool
	seenLeaves map[string]struct{}
}

func NewReceiverState(root, publicKey, signature string) *ReceiverState {
	return &ReceiverState{root: root, publicKey: publicKey, signature: signature, seenLeaves: make(map[string]struct{})}
}

func (state *ReceiverState) Validate(message *lib_types.UploadMessage, packet *merkle_dag.BatchedTransmissionPacket) error {
	if state.finalized {
		return fmt.Errorf("received a packet after finalization")
	}
	if message == nil || packet == nil || len(packet.Leaves) == 0 {
		return fmt.Errorf("received an empty transmission packet")
	}
	if message.Root != state.root {
		return fmt.Errorf("transmission root changed from %s to %s", state.root, message.Root)
	}
	if state.next == 0 {
		if packet.TotalPackets < 0 || packet.PacketIndex != 0 {
			return fmt.Errorf("invalid root packet counters index=%d total=%d", packet.PacketIndex, packet.TotalPackets)
		}
		if packet.TotalPackets > 0 {
			state.windowed = true
			state.total = packet.TotalPackets
		}
		rootLeaf := packet.GetRootLeaf()
		if rootLeaf == nil || rootLeaf.Hash != state.root {
			return fmt.Errorf("first packet does not contain the declared root leaf")
		}
	} else {
		if message.PublicKey != "" && message.PublicKey != state.publicKey {
			return fmt.Errorf("DAG public key changed after the root packet")
		}
		if message.Signature != "" && message.Signature != state.signature {
			return fmt.Errorf("DAG signature changed after the root packet")
		}
	}
	if state.windowed {
		if packet.TotalPackets != state.total {
			return fmt.Errorf("packet total changed from %d to %d", state.total, packet.TotalPackets)
		}
		if packet.PacketIndex != state.next {
			return fmt.Errorf("expected packet %d, received %d", state.next, packet.PacketIndex)
		}
		if packet.PacketIndex < 0 || packet.PacketIndex >= state.total {
			return fmt.Errorf("packet index %d is out of range for total %d", packet.PacketIndex, state.total)
		}
		if message.IsFinalPacket != (packet.PacketIndex == state.total-1) {
			return fmt.Errorf("packet %d final marker is inconsistent with total %d", packet.PacketIndex, state.total)
		}
	} else if packet.TotalPackets != 0 || packet.PacketIndex != 0 {
		return fmt.Errorf("legacy packet counters changed during transfer")
	}
	packetLeaves := make(map[string]struct{}, len(packet.Leaves))
	for _, leaf := range packet.Leaves {
		if leaf == nil || leaf.Hash == "" {
			return fmt.Errorf("packet contains a nil or unhashed leaf")
		}
		if _, duplicate := packetLeaves[leaf.Hash]; duplicate {
			return fmt.Errorf("packet repeats leaf %s", leaf.Hash)
		}
		if _, duplicate := state.seenLeaves[leaf.Hash]; duplicate {
			return fmt.Errorf("transfer repeats previously verified leaf %s", leaf.Hash)
		}
		parent, exists := packet.Relationships[leaf.Hash]
		if !exists {
			return fmt.Errorf("packet omits relationship for leaf %s", leaf.Hash)
		}
		if parent == "" && leaf.Hash != state.root {
			return fmt.Errorf("empty DAG parent declared for non-root leaf %s", leaf.Hash)
		}
		packetLeaves[leaf.Hash] = struct{}{}
	}
	return nil
}

func (state *ReceiverState) Commit(message *lib_types.UploadMessage, packet *merkle_dag.BatchedTransmissionPacket) {
	for _, leaf := range packet.Leaves {
		state.seenLeaves[leaf.Hash] = struct{}{}
	}
	state.next++
	state.finalized = message.IsFinalPacket
}

func (state *ReceiverState) Acknowledgment() string {
	if !state.windowed {
		return ""
	}
	return EncodeAck(state.next - 1)
}

func (state *ReceiverState) Windowed() bool { return state.windowed }
func (state *ReceiverState) Next() int      { return state.next }
func (state *ReceiverState) Total() int     { return state.total }
