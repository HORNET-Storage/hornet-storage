package sync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	stores_graviton "github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/bencode"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/nbd-wtf/go-nostr"
	"log"
	"reflect"
	"sort"
	"sync"
	"time"
)

type RelayStore struct {
	relays       map[string]NostrRelay
	selfRelay    NostrRelay
	libp2pHost   host.Host
	eventStore   *stores_graviton.GravitonStore
	mutex        sync.RWMutex
	dhtServer    *dht.Server
	uploadTicker *time.Ticker
	stopChan     chan struct{}
}

type KeyPair struct {
	PrivKey []byte
	PubKey  []byte
}

var HardcodedKey = KeyPair{
	PrivKey: []byte{
		0x51, 0x8d, 0x31, 0x74, 0x5e, 0x17, 0x14, 0x28,
		0xf4, 0xbc, 0x5e, 0x2c, 0x88, 0xae, 0x2f, 0x36,
		0x37, 0x7a, 0xc2, 0xf4, 0xd3, 0xe1, 0x38, 0x68,
		0xac, 0xc6, 0x9f, 0x3f, 0x88, 0x99, 0x2b, 0xdb,
		0x6b, 0x9f, 0x74, 0x78, 0x36, 0x89, 0x4f, 0xc2,
		0xc6, 0xcd, 0xbe, 0x8d, 0xce, 0x52, 0xc1, 0xaf,
		0xc1, 0xc9, 0x48, 0xb5, 0x72, 0xf0, 0xc6, 0x62,
		0x3a, 0x07, 0xcf, 0x77, 0xb5, 0xb8, 0xf8, 0x7f,
	},
	PubKey: []byte{
		0x6b, 0x9f, 0x74, 0x78, 0x36, 0x89, 0x4f, 0xc2,
		0xc6, 0xcd, 0xbe, 0x8d, 0xce, 0x52, 0xc1, 0xaf,
		0xc1, 0xc9, 0x48, 0xb5, 0x72, 0xf0, 0xc6, 0x62,
		0x3a, 0x07, 0xcf, 0x77, 0xb5, 0xb8, 0xf8, 0x7f,
	},
}

const MaxRelays = 4

func NewRelayStore(dhtServer *dht.Server, host host.Host, eventStore *stores_graviton.GravitonStore, uploadInterval time.Duration, self *NostrRelay) *RelayStore {
	rs := &RelayStore{
		relays:       make(map[string]NostrRelay),
		selfRelay:    *self,
		libp2pHost:   host,
		eventStore:   eventStore,
		dhtServer:    dhtServer,
		uploadTicker: time.NewTicker(uploadInterval),
		stopChan:     make(chan struct{}),
	}

	go rs.periodicSearchUploadSync()
	return rs
}

func (rs *RelayStore) AddRelay(relay NostrRelay) {
	rs.mutex.Lock()
	defer rs.mutex.Unlock()
	rs.relays[relay.ID] = relay
}

func (rs *RelayStore) GetRelays() []NostrRelay {
	rs.mutex.RLock()
	defer rs.mutex.RUnlock()
	relays := make([]NostrRelay, 0, len(rs.relays))
	for _, relay := range rs.relays {
		relays = append(relays, relay)
	}
	return relays
}

func (rs *RelayStore) GetSelfRelay() NostrRelay {
	rs.mutex.RLock()
	defer rs.mutex.RUnlock()
	return rs.selfRelay
}

func (rs *RelayStore) periodicSearchUploadSync() {
	for {
		select {
		case <-rs.uploadTicker.C:
			relays, unoccupied := SearchForRelays(rs.dhtServer, MaxRelays, 0, MaxRelays)
			if len(unoccupied) > 0 {
				err := rs.uploadToDHT(unoccupied[0])
				if err != nil {
					log.Printf("Error uploading to DHT: %v", err)
				}
			}
			for _, relay := range relays {
				rs.AddRelay(relay)
				rs.SyncWithRelay(&relay)
			}
		case <-rs.stopChan:
			rs.uploadTicker.Stop()
			return
		}
	}
}

func (rs *RelayStore) SyncWithRelay(relay *NostrRelay) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	addrs := []ma.Multiaddr{}
	for _, addr := range relay.Addrs {
		multiAddr, err := ma.NewMultiaddr(addr)
		if err == nil {
			addrs = append(addrs, multiAddr)
		} else {
			log.Printf("Error creating multiaddr from %v: %v", addr, err)
		}
	}

	target := peer.AddrInfo{ID: peer.ID(relay.ID), Addrs: addrs}
	if err := rs.libp2pHost.Connect(ctx, target); err != nil {
		log.Printf("Error connecting to %+v: %v", target, err)
	}

	// Open a stream to the peer
	stream, err := rs.libp2pHost.NewStream(ctx, target.ID, NegentropyProtocol)
	if err != nil {
		log.Printf("Error creating stream to %+v: %v", target, err)
	}

	err = InitiateEventSync(stream, nostr.Filter{}, target.ID.String(), rs.eventStore)
	if err != nil {
		log.Printf("Error syncing events with %+v: %v", target, err)
	}

	err = stream.Close()
	if err != nil {
		log.Printf("Failed to close stream: %v", err)
		return
	}

}

func SearchForRelays(d *dht.Server, maxRelays int, minIndex int, maxIndex int) ([]NostrRelay, []int) {
	log.Printf("Searching for relays from %d to %d", minIndex, maxIndex)
	type result struct {
		index int
		relay NostrRelay
		found bool
	}

	var relays []NostrRelay
	var unoccupiedSlots []int
	ch := make(chan result, maxIndex-minIndex)

	// Create a context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start multiple goroutines to search in parallel
	for i := minIndex; i < maxIndex; i++ {
		go func(i int) {
			// Create salt
			salt := []byte(fmt.Sprintf("nostr:relay:%d", i))
			target := createMutableTarget(HardcodedKey.PubKey, salt)
			fmt.Printf("get target %d: %x salt: %x\n", i, target, salt)

			// Perform DHT get operation
			data, err := DoGet(d, target, salt)
			if err != nil {
				ch <- result{index: i, found: false}
				return
			}

			foundRelay := NostrRelay{}
			err = json.Unmarshal(data, &foundRelay)
			if err != nil {
				fmt.Printf("Could not unmarshall into NostrRelay %x : %v\n", data, err)
				ch <- result{index: i, found: false}
				return
			}

			err = foundRelay.CheckSig()
			if err != nil {
				fmt.Printf("Signature verification failed %+v : %v\n", foundRelay, err)
				ch <- result{index: i, found: false}
				return
			}

			ch <- result{index: i, relay: foundRelay, found: true}
		}(i)
	}

	// Collect results
	foundCount := 0
	for i := 0; i < maxIndex-minIndex; i++ {
		//fmt.Printf("waiting for %d\n", i)
		select {
		case res := <-ch:
			if res.found { // Check if a relay was found
				//fmt.Printf("found %d\n", i)
				relays = append(relays, res.relay)
				foundCount++
			} else {
				//fmt.Printf("not found %d\n", i)
				unoccupiedSlots = append(unoccupiedSlots, res.index)
			}
			if foundCount >= maxRelays && len(unoccupiedSlots) > 0 {
				return relays, unoccupiedSlots
			}
		case <-ctx.Done():
			return relays[:foundCount], unoccupiedSlots
		}
	}

	// Trim any unfilled slots
	return relays[:foundCount], unoccupiedSlots
}

func (rs *RelayStore) uploadToDHT(freeSlot int) error {
	// Create a target for the DHT (you might want to use a more sophisticated key)
	salt := []byte(fmt.Sprintf("nostr:relay:%d", freeSlot))

	selfRelay := rs.GetSelfRelay()
	relayBytes, err := MarshalRelay(selfRelay)
	if err != nil {
		return err
	}

	target := DoPut(rs.dhtServer, relayBytes, salt, (*ed25519.PublicKey)(&HardcodedKey.PubKey), (*ed25519.PrivateKey)(&HardcodedKey.PrivKey))

	log.Printf("Successfully uploaded self relay to DHT at target %x", target)
	return nil
}

func (rs *RelayStore) Stop() {
	close(rs.stopChan)
}

func SignPut(put *bep44.Put, privKey ed25519.PrivateKey) error {
	if len(privKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key size: expected %d, got %d", ed25519.PrivateKeySize, len(privKey))
	}

	signatureInput, err := createSignatureInput(put)
	if err != nil {
		return err
	}

	signature := ed25519.Sign(privKey, signatureInput)
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature size: expected %d, got %d", ed25519.SignatureSize, len(signature))
	}

	if len(put.Sig) != len(signature) {
		return fmt.Errorf("put.Sig field has incorrect size: expected %d, got %d", len(signature), len(put.Sig))
	}

	copy(put.Sig[:], signature)
	return nil
}

// Helper function to create the input for signing
func createSignatureInput(put *bep44.Put) ([]byte, error) {
	var buf bytes.Buffer

	if len(put.Salt) > 0 {
		buf.WriteString(fmt.Sprintf("4:salt%d:", len(put.Salt)))
		buf.Write(put.Salt)
	}

	buf.WriteString(fmt.Sprintf("3:seqi%d", put.Seq))
	// Bencode already prefixes the length of V before writing it
	buf.WriteString(fmt.Sprintf("e1:v"))

	// Encode and write the value
	encoder := bencode.NewEncoder(&buf)
	err := encoder.Encode(put.V)
	if err != nil {
		return nil, fmt.Errorf("failed to encode value: %w", err)
	}

	log.Println(buf.String())
	return buf.Bytes(), nil
}

func createTarget(value []byte) krpc.ID {
	return sha1.Sum(bencode.MustMarshal(value))
}

func createMutableTarget(pubKey []byte, salt []byte) krpc.ID {
	return sha1.Sum(append(pubKey[:], salt...))
}

func MarshalRelay(nr NostrRelay) ([]byte, error) {
	// Create a map to hold our data
	m := make(map[string]interface{})

	// Use reflection to get all fields
	v := reflect.ValueOf(nr)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i).Interface()
		jsonTag := field.Tag.Get("json")
		if jsonTag != "" && jsonTag != "-" {
			m[jsonTag] = value
		}
	}

	// Get sorted keys
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Create a new map with sorted keys
	sorted := make(map[string]interface{})
	for _, k := range keys {
		sorted[k] = m[k]
	}

	// Marshal the sorted map
	return json.Marshal(sorted)
}

func DoPut(server *dht.Server, value []byte, salt []byte, pubKey *ed25519.PublicKey, privKey *ed25519.PrivateKey) krpc.ID {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var target krpc.ID
	if privKey == nil {
		target = createTarget(value)
		log.Printf("Derived immutable target %x from %x", target, value)
	} else {
		target = createMutableTarget(*pubKey, salt)
		log.Printf("Derived mutable target %x from %x and %x", target, pubKey, salt)
	}

	stats, err := getput.Put(ctx, target, server, salt, func(seq int64) bep44.Put {
		put := bep44.Put{
			V:    value,
			Salt: salt,
			Seq:  seq,
		}

		if privKey != nil {
			var pub [32]byte
			copy(pub[:], *pubKey)
			put.K = &pub
			err := SignPut(&put, *privKey)
			if err != nil {
				log.Printf("Unable to sign")
			}
		}

		log.Printf("Put created %+v", put)

		return put
	})

	log.Printf("Put stats %v", stats)

	if err != nil {
		log.Printf("Put operation failed: %v", err)
	} else {
		log.Printf("Put operation successful")
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		log.Printf("Put operation timed out")
	}

	return target
}

func DoGet(server *dht.Server, target bep44.Target, salt []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, stats, err := getput.Get(ctx, target, server, nil, salt)
	log.Printf("Get stats: %+v", stats)

	if err != nil {
		log.Printf("Get operation failed: %v", err)
		return nil, err
	}
	log.Printf("Get operation successful: %+v", result)

	var decodedValue []byte
	err = bencode.Unmarshal(result.V, &decodedValue)
	if err != nil {
		log.Printf("failed to unmarshal result: %v", err)
		return nil, err
	}

	return decodedValue, nil
}
