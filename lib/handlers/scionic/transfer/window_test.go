package transfer

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	lib_types "github.com/HORNET-Storage/hdk-nostr-go/lib"
	lib_stream "github.com/HORNET-Storage/hdk-nostr-go/lib/connmgr"
)

type windowTestStream struct {
	net.Conn
	ctx context.Context
}

func (stream *windowTestStream) Context() context.Context { return stream.ctx }

func windowTestPacket(index, total int) *merkle_dag.BatchedTransmissionPacket {
	hash := fmt.Sprintf("leaf-%d", index)
	parent := "root"
	if index == 0 {
		hash, parent = "root", ""
	}
	return &merkle_dag.BatchedTransmissionPacket{
		Leaves: []*merkle_dag.DagLeaf{{Hash: hash}}, Relationships: map[string]string{hash: parent}, PacketIndex: index, TotalPackets: total,
	}
}

func windowTestMessage(index, total int) lib_types.UploadMessage {
	message := lib_types.UploadMessage{Root: "root", Packet: *windowTestPacket(index, total).ToSerializable(), IsFinalPacket: index == total-1}
	if index == 0 {
		message.PublicKey, message.Signature = "pub", "sig"
	}
	return message
}

func TestSenderPipelinesWithinNegotiatedWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	clientConn, serverConn := net.Pipe()
	client := &windowTestStream{Conn: clientConn, ctx: ctx}
	server := &windowTestStream{Conn: serverConn, ctx: ctx}
	defer client.Close()
	defer server.Close()

	peerErr := make(chan error, 1)
	go func() {
		messageReader := lib_stream.NewMessageReader(server)
		if _, err := lib_stream.WaitForUploadMessageFromReader(messageReader); err != nil {
			peerErr <- err
			return
		}
		if err := lib_stream.WriteMessageToStream(server, lib_stream.BuildResponseMessage(true, "scionic-window/1 0 2 33554432")); err != nil {
			peerErr <- err
			return
		}
		if _, err := lib_stream.WaitForUploadMessageFromReader(messageReader); err != nil {
			peerErr <- err
			return
		}
		if _, err := lib_stream.WaitForUploadMessageFromReader(messageReader); err != nil {
			peerErr <- err
			return
		}
		if err := lib_stream.WriteMessageToStream(server, lib_stream.BuildResponseMessage(true, "scionic-window/1 1 2 33554432")); err != nil {
			peerErr <- err
			return
		}
		peerErr <- lib_stream.WriteMessageToStream(server, lib_stream.BuildResponseMessage(true, "scionic-window/1 2 2 33554432"))
	}()

	sender, err := NewSender(ctx, client, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	for index := range 3 {
		if err := sender.Send(windowTestMessage(index, 3), windowTestPacket(index, 3)); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := sender.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-peerErr; err != nil {
		t.Fatal(err)
	}
	if !stats.Enabled || stats.Packets != 2 {
		t.Fatalf("window negotiation failed: %+v", stats)
	}
}

func TestReceiverStateBindsRootAndFinalization(t *testing.T) {
	state := NewReceiverState("root", "pub", "sig")
	rootMessage := windowTestMessage(0, 2)
	rootPacket := windowTestPacket(0, 2)
	if err := state.Validate(&rootMessage, rootPacket); err != nil {
		t.Fatal(err)
	}
	state.Commit(&rootMessage, rootPacket)

	foreign := windowTestMessage(1, 2)
	foreign.Root = "foreign"
	if err := state.Validate(&foreign, windowTestPacket(1, 2)); err == nil {
		t.Fatal("root substitution was accepted")
	}

	final := windowTestMessage(1, 2)
	finalPacket := windowTestPacket(1, 2)
	if err := state.Validate(&final, finalPacket); err != nil {
		t.Fatal(err)
	}
	state.Commit(&final, finalPacket)
	if got := state.Acknowledgment(); got != "scionic-window/1 1 16 33554432" {
		t.Fatalf("unexpected final ACK: %q", got)
	}
	if err := state.Validate(&final, finalPacket); err == nil {
		t.Fatal("packet after finalization was accepted")
	}
}

func TestParseAckBounds(t *testing.T) {
	if ack, supported, err := ParseAck("scionic-window/1 4 16 33554432"); err != nil || !supported || ack.Index != 4 {
		t.Fatalf("valid ACK rejected: %+v %v %v", ack, supported, err)
	}
	if _, supported, err := ParseAck(""); err != nil || supported {
		t.Fatalf("legacy ACK misparsed: %v %v", supported, err)
	}
	if _, supported, err := ParseAck("scionic-window/1 0 16 268435457"); err == nil || !supported {
		t.Fatal("oversized byte window was accepted")
	}
}
