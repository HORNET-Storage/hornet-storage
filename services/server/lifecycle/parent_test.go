package lifecycle

import (
	"os"
	"testing"
	"time"
)

func TestStopChannelClosesWhenParentPipeCloses(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	stop := StopChannel(reader, true, os.Interrupt)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("parent pipe EOF did not close stop channel")
	}
}

func TestStopChannelIgnoresClosedParentWhenWatchDisabled(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	stop := StopChannel(reader, false, os.Interrupt)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-stop:
		t.Fatal("closed parent pipe stopped an unsupervised process")
	case <-time.After(50 * time.Millisecond):
	}
}
