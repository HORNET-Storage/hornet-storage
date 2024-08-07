package sync

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HORNET-Storage/go-hornet-storage-lib/lib"
	"github.com/HORNET-Storage/go-hornet-storage-lib/lib/connmgr"
	"github.com/HORNET-Storage/hornet-storage/lib/signing"
	stores_graviton "github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	"github.com/illuzen/go-negentropy"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/nbd-wtf/go-nostr"
	"io"
	"log"
	"strings"
	"time"
)

// TODO: where is this supposed to come from? config file?
const hornetNpub string = "npub1c25aedfd38f9fed72b383f6eefaea9f21dd58ec2c9989e0cc275cb5296adec17"

func SetupNegentropyEventHandler(h host.Host, hostId string, db *stores_graviton.GravitonStore) {
	handler := func(stream network.Stream) {
		handleIncomingNegentropyEventStream(stream, hostId, db)
	}
	h.SetStreamHandler(NegentropyProtocol, handler)
}

func handleIncomingNegentropyEventStream(stream network.Stream, hostId string, store *stores_graviton.GravitonStore) {
	defer stream.Close()

	// Log the incoming connection (optional)
	localPeer := stream.Conn().LocalPeer()
	remotePeer := stream.Conn().RemotePeer()
	log.Printf("Received negentropy sync request to %s from %s", localPeer, remotePeer)

	// Perform the negentropy sync
	err := listenNegentropy(&negentropy.Negentropy{}, stream, hostId, store, false)
	if err != nil {
		err = SendNegentropyMessage(hostId, stream, "NEG-ERR", nostr.Filter{}, []byte{}, err.Error(), []string{}, []byte{})
		return
	}

	log.Printf("Successfully completed negentropy sync with %s", remotePeer)
}

func LoadEventVector(events []*nostr.Event) (*negentropy.Vector, error) {
	vector := negentropy.NewVector()
	for _, event := range events {
		id, err := hex.DecodeString(event.ID)
		if err != nil {
			return nil, err
		}

		err = vector.Insert(uint64(event.CreatedAt), id[:IdSize])
		if err != nil {
			return nil, err
		}
	}

	err := vector.Seal()
	if err != nil {
		return nil, err
	}

	return vector, nil
}

func InitiateEventSync(stream network.Stream, filter nostr.Filter, hostId string, store *stores_graviton.GravitonStore) error {
	log.Printf("Performing negentropy on %s", hostId)
	events, err := store.QueryEvents(filter)
	if err != nil {
		return err
	}
	log.Printf("%s has %d events", hostId, len(events))

	// vector conforms to Storage interface, fill it with events
	vector, err := LoadEventVector(events)
	if err != nil {
		return err
	}

	log.Printf("%s sealed the events", hostId)

	neg, err := negentropy.NewNegentropy(vector, uint64(FrameSizeLimit))
	if err != nil {
		panic(err)
	}

	initialMsg, err := neg.Initiate()
	log.Printf("%s is initiating with version %d", hostId, initialMsg[0])

	err = SendNegentropyMessage(hostId, stream, "NEG-OPEN", filter, initialMsg, "", []string{}, []byte{})
	if err != nil {
		return err
	}

	err = listenNegentropy(neg, stream, hostId, store, true)
	return nil
}

func GetScionicRoot(event *nostr.Event) (string, bool) {
	for _, tag := range event.Tags {
		if len(tag) == 2 && tag[0] == "scionic_root" {
			return tag[1], true
		}
	}
	return "", false
}

func DownloadDag(root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Connect to a hornet storage node
	publicKey, err := signing.DeserializePublicKey(hornetNpub)
	if err != nil {
		log.Fatal(err)
	}

	libp2pPubKey, err := signing.ConvertPubKeyToLibp2pPubKey(publicKey)
	if err != nil {
		log.Fatal(err)
	}

	peerId, err := peer.IDFromPublicKey(*libp2pPubKey)
	if err != nil {
		log.Fatal(err)
	}

	conMgr := connmgr.NewGenericConnectionManager()

	err = conMgr.ConnectWithLibp2p(ctx, "default", fmt.Sprintf("/ip4/127.0.0.1/udp/9000/quic-v1/p2p/%s", peerId.String()), libp2p.Transport(libp2pquic.NewTransport))
	if err != nil {
		log.Fatal(err)
	}

	progressChan := make(chan lib.DownloadProgress)

	go func() {
		for progress := range progressChan {
			if progress.Error != nil {
				fmt.Printf("Error uploading to %s: %v\n", progress.ConnectionID, progress.Error)
			} else {
				fmt.Printf("Progress for %s: %d leafs downloaded\n", progress.ConnectionID, progress.LeafsRetreived)
			}
		}
	}()

	// Upload the dag to the hornet storage node
	_, dag, err := connmgr.DownloadDag(ctx, conMgr, "default", root, nil, nil, nil, progressChan)
	if err != nil {
		log.Fatal(err)
	}

	close(progressChan)

	// Verify the entire dag
	err = dag.Verify()
	if err != nil {
		log.Fatalf("Error: %s", err)
	}

	log.Println("Dag verified correctly")

	// Disconnect client as we no longer need it
	err = conMgr.Disconnect("default")
	if err != nil {
		log.Printf("Could not disconnect from hornet storage: %v", err)
	}
}

func listenNegentropy(neg *negentropy.Negentropy, stream network.Stream, hostId string, store *stores_graviton.GravitonStore, initiator bool) error {
	// Now, start listening to responses and reconcile
	reader := bufio.NewReader(stream)
	final := false

	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break // End of stream
			}
			return fmt.Errorf("error reading from stream: %w", err)
		}
		response = strings.TrimSpace(response)
		//log.Printf("%s received: %s", hostId, response)

		// Create a slice to hold the parsed data
		var parsedData []string

		// Unmarshal the JSON string into the slice
		err = json.Unmarshal([]byte(response), &parsedData)
		if err != nil {
			log.Println("Error parsing JSON:", err)
			return err
		}

		msgType := parsedData[0]
		log.Println(hostId, "Received:", msgType)

		switch msgType {
		case "NEG-OPEN":
			var filter nostr.Filter

			err := json.Unmarshal([]byte(parsedData[2]), &filter)
			if err != nil {
				log.Println("Error unmarshaling filter data:", err)
				return err
			}

			events, err := store.QueryEvents(filter)
			if err != nil {
				return err
			}
			log.Printf("%s has %d events", hostId, len(events))

			vector, err := LoadEventVector(events)
			if err != nil {
				return err
			}

			log.Printf("%s sealed the events", hostId)
			// intentional shadowing
			neg, err = negentropy.NewNegentropy(vector, uint64(FrameSizeLimit))
			if err != nil {
				return err
			}
			decodedBytes, err := hex.DecodeString(parsedData[4])
			msg, err := neg.Reconcile(decodedBytes)
			if err != nil {
				return err
			}

			err = SendNegentropyMessage(hostId, stream, "NEG-MSG", nostr.Filter{}, msg, "", []string{}, []byte{})
			if err != nil {
				return err
			}
			break
		case "NEG-MSG":
			decodedBytes, err := hex.DecodeString(parsedData[2])
			var msg []byte
			var have, need []string

			if initiator {
				msg, err = neg.ReconcileWithIDs(decodedBytes, &have, &need)
				if err != nil {
					return err
				}
				log.Println(hostId, "have", len(have), "need", len(need))
				//log.Println(hostId, "have:", have, "need:", need)

				// upload have
				if len(have) > 0 {
					hexHave := make([]string, len(have))
					for i, s := range have {
						hexHave[i] = hex.EncodeToString([]byte(s))
					}

					filter := nostr.Filter{
						IDs: hexHave,
					}

					haveEvents, err := store.QueryEvents(filter)
					if err != nil {
						return err
					}
					//log.Println(haveEvents)

					// Marshal the array of events to JSON
					haveBytes, err := json.Marshal(haveEvents)
					if err != nil {
						log.Println("Error marshaling to JSON:", err)
						return err
					}

					// upload
					err = SendNegentropyMessage(hostId, stream, "NEG-HAVE", nostr.Filter{}, []byte{}, "", []string{}, haveBytes)
					if err != nil {
						return err
					}
				}

				// download need if needed
				if len(need) > 0 {
					needIds := make([]string, len(need))
					for i, s := range need {
						needIds[i] = hex.EncodeToString([]byte(s))
					}
					err = SendNegentropyMessage(hostId, stream, "NEG-NEED", nostr.Filter{}, []byte{}, "", needIds, []byte{})
					if err != nil {
						return err
					}
				}
			} else {
				msg, err = neg.Reconcile(decodedBytes)
			}
			if err != nil {
				return err
			}

			if len(msg) == 0 {
				log.Println(hostId, ": Sync complete")
				if len(need) == 0 {
					// we are done
					err = SendNegentropyMessage(hostId, stream, "NEG-CLOSE", nostr.Filter{}, []byte{}, "", []string{}, []byte{})
					if err != nil {
						return err
					}
					return nil
				} else {
					// we are waiting for final needs
					final = true
				}
			} else {
				log.Println(hostId, ": Sync incomplete, drilling down")
				err = SendNegentropyMessage(hostId, stream, "NEG-MSG", nostr.Filter{}, msg, "", []string{}, []byte{})
				if err != nil {
					return err
				}
			}
			break

		case "NEG-HAVE":
			var newEvents []*nostr.Event
			err = json.Unmarshal([]byte(parsedData[2]), &newEvents)
			if err != nil {
				return err
			}
			for _, event := range newEvents {
				err := store.StoreEvent(event)
				if err != nil {
					log.Printf("Could not store event %+v skipping", event)
					continue
				}
				if event.Kind == 117 {
					// do leaf sync
					root, found := GetScionicRoot(event)
					if found == false {
						log.Printf("Event of type 117 with no 'scionic_root' tag, skipping tree download %+v", event)
						continue
					}
					
					DownloadDag(root)
				}
			}
			if final {
				err = SendNegentropyMessage(hostId, stream, "NEG-CLOSE", nostr.Filter{}, []byte{}, "", []string{}, []byte{})
				if err != nil {
					return err
				}
				return nil
			}
		case "NEG-NEED":
			var needIds []string

			err = json.Unmarshal([]byte(parsedData[2]), &needIds)
			//log.Printf("other hos t sent %s\n", needIds)

			filter := nostr.Filter{
				IDs: needIds,
			}
			//log.Printf("other host needs %d\n", len(needIds))

			haveEvents, err := store.QueryEvents(filter)
			if err != nil {
				return err
			}

			// Marshal the array of events to JSON
			haveBytes, err := json.Marshal(haveEvents)
			if err != nil {
				log.Println("Error marshaling to JSON:", err)
				return err
			}

			// upload
			err = SendNegentropyMessage(hostId, stream, "NEG-HAVE", nostr.Filter{}, []byte{}, "", []string{}, haveBytes)
			if err != nil {
				log.Println(hostId, "Error uploading", err)
				return err
			}

		case "NEG-ERR":
			return nil
		case "NEG-CLOSE":
			return nil
		default:
			return errors.New("unknown message type")
		}
	}
	return nil
}
