package main

import (
	"fmt"
	"github.com/HORNET-Storage/hornet-storage/lib/signing"
	negentropy "github.com/HORNET-Storage/hornet-storage/lib/sync"
	"github.com/anacrolix/dht/v2"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/query"
	stores_graviton "github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/libp2p"
)

func init() {
	viper.SetDefault("web", false)
	viper.SetDefault("proxy", true)
	viper.SetDefault("port", "9000")
	viper.SetDefault("relay_stats_db", "relay_stats.db")
	viper.SetDefault("query_cache", map[string]string{})
	viper.SetDefault("service_tag", "hornet-storage-service")
	viper.SetDefault("relay_name", fmt.Sprintf("hornet-storage-%d", rand.Intn(1000000)))
	viper.SetDefault("relay_priv_key", "")
	viper.SetDefault("relay_pub_key", "")
	viper.SetDefault("supported_nips", []int{0, 1, 3, 5, 6, 7, 8, 97, 1984, 9735, 9372, 9373, 9802, 10000, 10001, 10002, 30000, 30008, 30009, 30023, 30079})

	viper.AddConfigPath(".")
	viper.SetConfigType("json")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			viper.SafeWriteConfig()
		}
	}

	viper.OnConfigChange(func(e fsnotify.Event) {
		fmt.Println("Config file changed:", e.Name)
	})

	viper.WatchConfig()
}

func main() {
	wg := new(sync.WaitGroup)

	// Private key
	key := viper.GetString("relay_priv_key")

	host := libp2p.GetHost(key)

	// Create and initialize database
	store := &stores_graviton.GravitonStore{}

	queryCache := viper.GetStringMapString("query_cache")
	// TODO: can graviton handle multiple simultaneous applications in the same db
	err := store.InitStore("gravitondb", queryCache)
	if err != nil {
		log.Fatal(err)
	}

	query.AddQueryHandler(host, store)

	//settings, err := nostr.LoadRelaySettings()
	//if err != nil {
	//	log.Fatalf("Failed to load relay settings: %v", err)
	//	return
	//}

	config := dht.NewDefaultServerConfig()
	dhtServer, err := dht.NewServer(config)
	if err != nil {
		log.Fatal(err)
	}
	defer dhtServer.Close()

	log.Printf("Starting DHT bootstrap")
	_, err = dhtServer.Bootstrap()
	if err != nil {
		log.Fatal(err)
	}

	//Wait for nodes to be added to the routing table
	for i := 0; i < 30; i++ {
		stats := dhtServer.Stats()
		log.Printf("DHT stats: %+v", stats)
		if stats.GoodNodes > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	negentropy.SetupNegentropyEventHandler(host, "host", store)
	privKey, pubKey, err := signing.DeserializePrivateKey(viper.GetString("relay_priv_key"))
	if err != nil {
		log.Fatal(err)
	}

	selfRelay, err := negentropy.CreateSelfRelay(
		host.ID().String(),
		host.Addrs(),
		viper.GetString("relay_name"),
		pubKey.SerializeCompressed(),
		privKey,
		viper.GetIntSlice("supported_nips"),
	)

	// this periodically searches dht for other relays, stores them, attempts to sync with them, and uploads self to dht
	relayStore := negentropy.NewRelayStore(dhtServer, host, store, time.Minute*1, selfRelay)
	log.Printf("Created relay store: %+v", relayStore)

	wg.Wait()
}
