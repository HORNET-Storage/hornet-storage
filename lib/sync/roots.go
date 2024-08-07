package sync

//
//import (
//	"bufio"
//	"encoding/hex"
//	"encoding/json"
//	"errors"
//	"fmt"
//	stores_graviton "github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
//	"github.com/illuzen/go-negentropy"
//	"github.com/libp2p/go-libp2p/core/host"
//	"github.com/libp2p/go-libp2p/core/network"
//	"github.com/nbd-wtf/go-nostr"
//	"io"
//	"log"
//	"strings"
//)
//
//func SetupNegentropyRootHandler(h host.Host, hostId string, db *stores_graviton.GravitonStore) {
//	handler := func(stream network.Stream) {
//		handleIncomingNegentropyRootStream(stream, hostId, db)
//	}
//	h.SetStreamHandler(NegentropyProtocol, handler)
//}
//
//func handleIncomingNegentropyRootStream(stream network.Stream, hostId string, store *stores_graviton.GravitonStore) {
//	defer stream.Close()
//
//	// Log the incoming connection (optional)
//	localPeer := stream.Conn().LocalPeer()
//	remotePeer := stream.Conn().RemotePeer()
//	log.Printf("Received negentropy sync request to %s from %s", localPeer, remotePeer)
//
//	// Perform the negentropy sync
//	err := listenRootNegentropy(&negentropy.Negentropy{}, stream, hostId, store, false)
//	if err != nil {
//		err = SendNegentropyMessage(hostId, stream, "NEG-ERR", nostr.Filter{}, []byte{}, err.Error(), []string{}, []byte{})
//		return
//	}
//
//	log.Printf("Successfully completed negentropy sync with %s", remotePeer)
//}
//
//func LoadRootVector(roots []stores_graviton.RootInfo) (*negentropy.Vector, error) {
//	vector := negentropy.NewVector()
//	for _, root := range roots {
//		id, err := hex.DecodeString(root.Hash)
//		if err != nil {
//			return nil, err
//		}
//
//		// TODO: WE NEED TIMESTAMPS HERE
//		err = vector.Insert(root.Timestamp, id[:IdSize])
//		if err != nil {
//			return nil, err
//		}
//	}
//
//	err := vector.Seal()
//	if err != nil {
//		return nil, err
//	}
//
//	return vector, nil
//}
//
//func InitiateRootSync(stream network.Stream, hostId string, store *stores_graviton.GravitonStore) error {
//	log.Printf("Performing negentropy on %s", hostId)
//	roots, err := store.GetRoots([]string{})
//	if err != nil {
//		return err
//	}
//	log.Printf("%s has %d roots", hostId, len(roots))
//
//	// vector conforms to Storage interface, fill it with events
//	vector, err := LoadRootVector(roots)
//	if err != nil {
//		return err
//	}
//
//	log.Printf("%s sealed the events", hostId)
//
//	neg, err := negentropy.NewNegentropy(vector, uint64(FrameSizeLimit))
//	if err != nil {
//		panic(err)
//	}
//
//	initialMsg, err := neg.Initiate()
//	log.Printf("%s is initiating with version %d", hostId, initialMsg[0])
//
//	err = SendNegentropyMessage(hostId, stream, "NEG-OPEN", nostr.Filter{}, initialMsg, "", []string{}, []byte{})
//	if err != nil {
//		return err
//	}
//
//	err = listenRootNegentropy(neg, stream, hostId, store, true)
//	return nil
//}
//
//func listenRootNegentropy(neg *negentropy.Negentropy, stream network.Stream, hostId string, store *stores_graviton.GravitonStore, initiator bool) error {
//	// Now, start listening to responses and reconcile
//	reader := bufio.NewReader(stream)
//	final := false
//
//	for {
//		response, err := reader.ReadString('\n')
//		if err != nil {
//			if err == io.EOF {
//				break // End of stream
//			}
//			return fmt.Errorf("error reading from stream: %w", err)
//		}
//		response = strings.TrimSpace(response)
//		//log.Printf("%s received: %s", hostId, response)
//
//		// Create a slice to hold the parsed data
//		var parsedData []string
//
//		// Unmarshal the JSON string into the slice
//		err = json.Unmarshal([]byte(response), &parsedData)
//		if err != nil {
//			log.Println("Error parsing JSON:", err)
//			return err
//		}
//
//		msgType := parsedData[0]
//		log.Println(hostId, "Received:", msgType)
//
//		switch msgType {
//		case "NEG-OPEN":
//			roots, err := store.GetRoots([]string{})
//			if err != nil {
//				return err
//			}
//			log.Printf("%s has %d roots", hostId, len(roots))
//
//			vector, err := LoadRootVector(roots)
//			if err != nil {
//				return err
//			}
//
//			log.Printf("%s sealed the events", hostId)
//			// intentional shadowing
//			neg, err = negentropy.NewNegentropy(vector, uint64(FrameSizeLimit))
//			if err != nil {
//				return err
//			}
//			decodedBytes, err := hex.DecodeString(parsedData[4])
//			msg, err := neg.Reconcile(decodedBytes)
//			if err != nil {
//				return err
//			}
//
//			err = SendNegentropyMessage(hostId, stream, "NEG-MSG", nostr.Filter{}, msg, "", []string{}, []byte{})
//			if err != nil {
//				return err
//			}
//			break
//		case "NEG-MSG":
//			decodedBytes, err := hex.DecodeString(parsedData[2])
//			var msg []byte
//			var have, need []string
//
//			if initiator {
//				msg, err = neg.ReconcileWithIDs(decodedBytes, &have, &need)
//				if err != nil {
//					return err
//				}
//				log.Println(hostId, "have", len(have), "need", len(need))
//				//log.Println(hostId, "have:", have, "need:", need)
//
//				// upload have
//				if len(have) > 0 {
//
//					haveEvents, err := store.GetRoots(have)
//					if err != nil {
//						return err
//					}
//					//log.Println(haveEvents)
//
//					// Marshal the array of events to JSON
//					haveBytes, err := json.Marshal(haveEvents)
//					if err != nil {
//						log.Println("Error marshaling to JSON:", err)
//						return err
//					}
//
//					// upload
//					err = SendNegentropyMessage(hostId, stream, "NEG-HAVE", nostr.Filter{}, []byte{}, "", []string{}, haveBytes)
//					if err != nil {
//						return err
//					}
//				}
//
//				// download need if needed
//				if len(need) > 0 {
//					needIds := make([]string, len(need))
//					for i, s := range need {
//						needIds[i] = hex.EncodeToString([]byte(s))
//					}
//					err = SendNegentropyMessage(hostId, stream, "NEG-NEED", nostr.Filter{}, []byte{}, "", needIds, []byte{})
//					if err != nil {
//						return err
//					}
//				}
//			} else {
//				msg, err = neg.Reconcile(decodedBytes)
//			}
//			if err != nil {
//				return err
//			}
//
//			if len(msg) == 0 {
//				log.Println(hostId, ": Sync complete")
//				if len(need) == 0 {
//					// we are done
//					err = SendNegentropyMessage(hostId, stream, "NEG-CLOSE", nostr.Filter{}, []byte{}, "", []string{}, []byte{})
//					if err != nil {
//						return err
//					}
//					return nil
//				} else {
//					// we are waiting for final needs
//					final = true
//				}
//			} else {
//				log.Println(hostId, ": Sync incomplete, drilling down")
//				err = SendNegentropyMessage(hostId, stream, "NEG-MSG", nostr.Filter{}, msg, "", []string{}, []byte{})
//				if err != nil {
//					return err
//				}
//			}
//			break
//
//		case "NEG-HAVE":
//			var newRoots []stores_graviton.RootInfo
//			err = json.Unmarshal([]byte(parsedData[2]), &newRoots)
//			if err != nil {
//				return err
//			}
//			err := store.PutRoots(newRoots)
//			if err != nil {
//				return err
//			}
//
//			if final {
//				err = SendNegentropyMessage(hostId, stream, "NEG-CLOSE", nostr.Filter{}, []byte{}, "", []string{}, []byte{})
//				if err != nil {
//					return err
//				}
//				return nil
//			}
//		case "NEG-NEED":
//			var needIds []string
//
//			err = json.Unmarshal([]byte(parsedData[2]), &needIds)
//			//log.Printf("other hos t sent %s\n", needIds)
//			//log.Printf("other host needs %d\n", len(needIds))
//
//			haveRoots, err := store.GetRoots(needIds)
//			if err != nil {
//				return err
//			}
//
//			// Marshal the array of roots to JSON
//			haveBytes, err := json.Marshal(haveRoots)
//			if err != nil {
//				log.Println("Error marshaling to JSON:", err)
//				return err
//			}
//
//			// upload
//			err = SendNegentropyMessage(hostId, stream, "NEG-HAVE", nostr.Filter{}, []byte{}, "", []string{}, haveBytes)
//			if err != nil {
//				log.Println(hostId, "Error uploading", err)
//				return err
//			}
//
//		case "NEG-ERR":
//			return nil
//		case "NEG-CLOSE":
//			return nil
//		default:
//			return errors.New("unknown message type")
//		}
//	}
//	return nil
//}
