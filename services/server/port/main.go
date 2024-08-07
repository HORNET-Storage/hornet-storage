package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	merkle_dag "github.com/HORNET-Storage/scionic-merkletree/dag"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/fsnotify/fsnotify"
	"github.com/ipfs/go-cid"
	"github.com/spf13/viper"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	//"github.com/libp2p/go-libp2p/p2p/security/noise"
	//libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

	"github.com/HORNET-Storage/hornet-storage/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/libp2p"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
	"github.com/HORNET-Storage/hornet-storage/lib/web"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/count"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/filter"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind0"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10000"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10001"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind10002"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind1984"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind3"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30000"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30008"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30009"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30023"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind30079"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind5"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind6"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind7"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind8"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9372"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9373"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9735"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/kind9802"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/universal"

	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/download"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/query"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/scionic/upload"

	//stores_memory "github.com/HORNET-Storage/hornet-storage/lib/stores/memory"
	stores_graviton "github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	//negentropy "github.com/illuzen/go-negentropy"
)

func init() {
	viper.SetDefault("key", "")
	viper.SetDefault("web", false)
	viper.SetDefault("proxy", true)
	viper.SetDefault("port", "9000")
	viper.SetDefault("relay_stats_db", "relay_stats.db")
	viper.SetDefault("query_cache", map[string]string{})
	viper.SetDefault("service_tag", "hornet-storage-service")

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
	key := viper.GetString("key")

	host := libp2p.GetHostOnPort(key, viper.GetString("port"))

	// Create and initialize database
	store := &stores_graviton.GravitonStore{}

	queryCache := viper.GetStringMapString("query_cache")
	store.InitStore("gravitondb", queryCache)

	// Stream Handlers
	download.AddDownloadHandler(host, store, func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		return true
	})

	upload.AddUploadHandler(host, store, func(rootLeaf *merkle_dag.DagLeaf, pubKey *string, signature *string) bool {
		decodedSignature, err := hex.DecodeString(*signature)
		if err != nil {
			return false
		}

		parsedSignature, err := schnorr.ParseSignature(decodedSignature)
		if err != nil {
			return false
		}

		cid, err := cid.Parse(rootLeaf.Hash)
		if err != nil {
			return false
		}

		publicKey, err := signing.DeserializePublicKey(*pubKey)
		if err != nil {
			return false
		}

		err = signing.VerifyCIDSignature(parsedSignature, cid, publicKey)
		return err == nil
	}, func(dag *merkle_dag.Dag, pubKey *string) {})

	query.AddQueryHandler(host, store)

	settings, err := nostr.LoadRelaySettings()
	if err != nil {
		log.Fatalf("Failed to load relay settings: %v", err)
		return
	}

	// Register Our Nostr Stream Handlers
	if settings.Mode == "unlimited" {
		nostr.RegisterHandler("universal", universal.BuildUniversalHandler(store))
	} else if settings.Mode == "smart" {
		nostr.RegisterHandler("kind/0", kind0.BuildKind0Handler(store))
		nostr.RegisterHandler("kind/1", kind1.BuildKind1Handler(store))
		nostr.RegisterHandler("kind/3", kind3.BuildKind3Handler(store))
		nostr.RegisterHandler("kind/5", kind5.BuildKind5Handler(store))
		nostr.RegisterHandler("kind/6", kind6.BuildKind6Handler(store))
		nostr.RegisterHandler("kind/7", kind7.BuildKind7Handler(store))
		nostr.RegisterHandler("kind/8", kind8.BuildKind8Handler(store))
		nostr.RegisterHandler("kind/1984", kind1984.BuildKind1984Handler(store))
		nostr.RegisterHandler("kind/9735", kind9735.BuildKind9735Handler(store))
		nostr.RegisterHandler("kind/9372", kind9372.BuildKind9372Handler(store))
		nostr.RegisterHandler("kind/9373", kind9373.BuildKind9373Handler(store))
		nostr.RegisterHandler("kind/9802", kind9802.BuildKind9802Handler(store))
		nostr.RegisterHandler("kind/10000", kind10000.BuildKind10000Handler(store))
		nostr.RegisterHandler("kind/10001", kind10001.BuildKind10001Handler(store))
		nostr.RegisterHandler("kind/10002", kind10002.BuildKind10002Handler(store))
		nostr.RegisterHandler("kind/30000", kind30000.BuildKind30000Handler(store))
		nostr.RegisterHandler("kind/30008", kind30008.BuildKind30008Handler(store))
		nostr.RegisterHandler("kind/30009", kind30009.BuildKind30009Handler(store))
		nostr.RegisterHandler("kind/30023", kind30023.BuildKind30023Handler(store))
		nostr.RegisterHandler("kind/30079", kind30079.BuildKind30079Handler(store))
	}

	nostr.RegisterHandler("filter", filter.BuildFilterHandler(store))
	nostr.RegisterHandler("count", count.BuildCountsHandler(store))

	// Auth event not supported for the libp2p connections yet
	//nostr.RegisterHandler("auth", auth.BuildAuthHandler(store))

	// Register a libp2p handler for every stream handler
	for kind := range nostr.GetHandlers() {
		handler := nostr.GetHandler(kind)

		wrapper := func(stream network.Stream) {
			read := func() ([]byte, error) {
				decoder := json.NewDecoder(stream)

				var rawMessage json.RawMessage
				err := decoder.Decode(&rawMessage)
				if err != nil {
					return nil, err
				}

				return rawMessage, nil
			}

			write := func(messageType string, params ...interface{}) {
				response := nostr.BuildResponse(messageType, params)

				if len(response) > 0 {
					stream.Write(response)
				}
			}

			handler(read, write)

			stream.Close()
		}

		host.SetStreamHandler(protocol.ID("/nostr/event/"+kind), wrapper)
	}

	// Web Panel
	if viper.GetBool("web") {
		wg.Add(1)

		fmt.Println("Starting with web server enabled")

		go func() {
			err := web.StartServer()

			if err != nil {
				fmt.Println("Fatal error occurred in web server")
			}

			wg.Done()
		}()
	}

	// Proxy web sockets
	if viper.GetBool("proxy") {
		wg.Add(1)

		fmt.Println("Starting with legacy nostr proxy web server enabled")

		go func() {
			err := websocket.StartServer(store)

			if err != nil {
				fmt.Println("Fatal error occurred in web server")
			}

			wg.Done()
		}()
	}

	fmt.Printf("Host started with id: %s\n", host.ID())
	fmt.Printf("Host started with address: %s\n", host.Addrs())

	wg.Wait()
}
