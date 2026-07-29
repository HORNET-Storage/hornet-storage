package nostr_relay

import (
	"testing"

	jsoniter "github.com/json-iterator/go"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"

	"github.com/HORNET-Storage/hornet-storage/lib/config"
	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/relaylisteners"
)

func TestHandleEventRoutesConfigAllowedKindToUniversal(t *testing.T) {
	viper.Reset()
	lib_nostr.ClearHandlers()
	t.Cleanup(func() {
		viper.Reset()
		lib_nostr.ClearHandlers()
		config.InitConfigForTesting()
	})

	viper.Set("event_filtering.allow_unregistered_kinds", false)
	viper.Set("event_filtering.registered_kinds", []int{73})
	viper.Set("event_filtering.kind_whitelist", []string{"kind73"})
	config.InitConfigForTesting()

	calledUniversal := false
	lib_nostr.RegisterHandler("universal", func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
		calledUniversal = true
		write("OK", "event-id", true, "handled")
	})

	var ok bool
	accepted := handleEvent(&nostr.EventEnvelope{
		Event: nostr.Event{
			ID:     "event-id",
			Kind:   73,
			PubKey: "pubkey",
		},
	}, func(messageType string, params ...interface{}) {
		if messageType == "OK" && len(params) >= 2 {
			ok, _ = params[1].(bool)
		}
	}, nil, jsoniter.ConfigCompatibleWithStandardLibrary)

	if !calledUniversal {
		t.Fatal("expected config-allowed kind without a specific handler to use universal handler")
	}
	if !ok || !accepted {
		t.Fatal("expected universal handler acknowledgement to mark the event accepted")
	}
}

func TestHandleReqRemainsLiveAfterEOSEUntilClose(t *testing.T) {
	lib_nostr.ClearHandlers()
	t.Cleanup(lib_nostr.ClearHandlers)

	lib_nostr.RegisterHandler("filter", func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
		write("EOSE", "repo-sub", "End of stored events")
	})

	broker := relaylisteners.NewBroker(4)
	var delivered []nostr.EventEnvelope
	connection := broker.Register(func(envelope nostr.EventEnvelope) error {
		delivered = append(delivered, envelope)
		return nil
	})
	defer connection.Close()

	eoseSeen := false
	write := func(messageType string, params ...interface{}) {
		if messageType == "EOSE" {
			eoseSeen = true
		}
	}
	handleReq(&nostr.ReqEnvelope{
		SubscriptionID: "repo-sub",
		Filters:        nostr.Filters{{Kinds: []int{73}}},
	}, write, jsoniter.ConfigCompatibleWithStandardLibrary, &dhtAuthState{}, connection)

	if !eoseSeen {
		t.Fatal("expected stored-event phase to finish with EOSE")
	}

	first := &nostr.Event{ID: "first", Kind: 73, PubKey: "author"}
	broker.Dispatch(first)
	if len(delivered) != 1 {
		t.Fatalf("expected one live event after EOSE, got %d", len(delivered))
	}
	if delivered[0].SubscriptionID == nil || *delivered[0].SubscriptionID != "repo-sub" {
		t.Fatalf("expected live event to retain subscription id, got %#v", delivered[0].SubscriptionID)
	}

	connection.RemoveSubscription("repo-sub")
	broker.Dispatch(&nostr.Event{ID: "second", Kind: 73, PubKey: "author"})
	if len(delivered) != 1 {
		t.Fatalf("expected CLOSE to stop live delivery, got %d events", len(delivered))
	}
}
