package relaylisteners

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

func liveTestEvent(kind int, id string, repository string) *nostr.Event {
	return &nostr.Event{
		ID:        id,
		PubKey:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt: 100,
		Kind:      kind,
		Tags:      nostr.Tags{{"r", repository}},
		Content:   "repository event",
	}
}

func TestBrokerDeliversMatchingAnonymousSubscriptions(t *testing.T) {
	broker := NewBroker(4)
	broker.store = nil
	var envelopes []nostr.EventEnvelope
	connection := broker.Register(func(envelope nostr.EventEnvelope) error {
		envelopes = append(envelopes, envelope)
		return nil
	})
	t.Cleanup(connection.Close)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, connection.Subscribe("repo", nostr.Filters{{Kinds: []int{73}, Tags: nostr.TagMap{"r": []string{"repo-guid"}}}}, cancel))
	broker.Dispatch(liveTestEvent(73, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repo-guid"))
	broker.Dispatch(liveTestEvent(74, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "repo-guid"))

	require.Len(t, envelopes, 1)
	require.Equal(t, "repo", *envelopes[0].SubscriptionID)
	require.Equal(t, 73, envelopes[0].Event.Kind)
	select {
	case <-ctx.Done():
		t.Fatal("an active subscription was cancelled")
	default:
	}
}

func TestConnectionMultiplexesReplacesAndClosesSubscriptions(t *testing.T) {
	broker := NewBroker(4)
	var delivered []string
	connection := broker.Register(func(envelope nostr.EventEnvelope) error {
		delivered = append(delivered, *envelope.SubscriptionID)
		return nil
	})
	t.Cleanup(connection.Close)

	var firstCancelled atomic.Bool
	firstCancel := func() { firstCancelled.Store(true) }
	require.NoError(t, connection.Subscribe("same", nostr.Filters{{Kinds: []int{73}}}, firstCancel))
	require.NoError(t, connection.Subscribe("same", nostr.Filters{{Kinds: []int{74}}}, func() {}))
	require.True(t, firstCancelled.Load(), "replacing REQ must cancel the old subscription")
	require.NoError(t, connection.Subscribe("other", nostr.Filters{{Kinds: []int{73}}}, func() {}))

	broker.Dispatch(liveTestEvent(73, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repo"))
	require.Equal(t, []string{"other"}, delivered)
	require.True(t, connection.RemoveSubscription("other"))
	require.False(t, connection.RemoveSubscription("missing"))
	broker.Dispatch(liveTestEvent(73, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "repo"))
	require.Equal(t, []string{"other"}, delivered)
}

func TestConnectionTeardownCancelsEverySubscriptionAndStopsDelivery(t *testing.T) {
	broker := NewBroker(4)
	var delivered atomic.Int32
	connection := broker.Register(func(nostr.EventEnvelope) error {
		delivered.Add(1)
		return nil
	})
	var cancelled atomic.Int32
	require.NoError(t, connection.Subscribe("one", nostr.Filters{{Kinds: []int{73}}}, func() { cancelled.Add(1) }))
	require.NoError(t, connection.Subscribe("two", nostr.Filters{{Kinds: []int{73}}}, func() { cancelled.Add(1) }))
	connection.Close()
	connection.Close()

	require.Equal(t, int32(2), cancelled.Load())
	broker.Dispatch(liveTestEvent(73, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repo"))
	require.Zero(t, delivered.Load())
	require.ErrorIs(t, connection.Subscribe("late", nostr.Filters{{Kinds: []int{73}}}, func() {}), ErrConnectionClosed)
}

func TestSenderFailureRemovesConnection(t *testing.T) {
	broker := NewBroker(4)
	sentinel := errors.New("transport closed")
	connection := broker.Register(func(nostr.EventEnvelope) error { return sentinel })
	var cancelled atomic.Bool
	require.NoError(t, connection.Subscribe("repo", nostr.Filters{{Kinds: []int{73}}}, func() { cancelled.Store(true) }))
	broker.Dispatch(liveTestEvent(73, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repo"))
	require.True(t, cancelled.Load())
	require.ErrorIs(t, connection.Authenticate("pubkey"), ErrConnectionClosed)
}
