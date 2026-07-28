package filter

import (
	"fmt"

	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/sessions"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/subscription"
	"github.com/HORNET-Storage/hornet-storage/lib/transports/websocket"
	jsoniter "github.com/json-iterator/go"
	"github.com/nbd-wtf/go-nostr"
	"github.com/spf13/viper"

	lib_nostr "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr"
	"github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/search"
	eventvisibility "github.com/HORNET-Storage/hornet-storage/lib/handlers/nostr/visibility"
)

// getAuthenticatedPubkey attempts to extract the authenticated pubkey from request data
// This function first checks if the data contains an auth wrapper, then falls back to sessions
func getAuthenticatedPubkey(data []byte) string {
	// First, try to extract pubkey from the wrapper structure
	var wrapper struct {
		Request         *nostr.ReqEnvelope `json:"request"`
		AuthPubkey      string             `json:"auth_pubkey"`
		IsAuthenticated bool               `json:"is_authenticated"`
	}

	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	if err := json.Unmarshal(data, &wrapper); err == nil {
		logging.Debugf("Wrapper unmarshaled successfully - AuthPubkey: '%s', IsAuthenticated: %v", wrapper.AuthPubkey, wrapper.IsAuthenticated)
		if wrapper.IsAuthenticated && wrapper.AuthPubkey != "" {
			logging.Debugf("Using authenticated pubkey from request wrapper: %s", wrapper.AuthPubkey)
			return wrapper.AuthPubkey
		} else {
			logging.Debugf("Wrapper found but not authenticated or empty pubkey")
		}
	} else {
		logging.Debugf("Failed to unmarshal wrapper structure: %v", err)
	}

	// If we couldn't extract from wrapper, fall back to the old method
	// Find currently authenticated pubkeys by scanning the session store
	logging.Debugf("Falling back to session store check...")
	var authenticatedPubkeys []string
	sessions.Sessions.Range(func(key, value interface{}) bool {
		pubkey, ok := key.(string)
		if !ok {
			return true // continue
		}

		session, ok := value.(*sessions.Session)
		if !ok {
			return true // continue
		}

		if session.Authenticated {
			authenticatedPubkeys = append(authenticatedPubkeys, pubkey)
			logging.Debugf("Found authenticated pubkey in sessions: %s", pubkey)
		}

		return true // continue
	})

	// Log the authenticated pubkeys we found for debugging
	if len(authenticatedPubkeys) > 0 {
		logging.Debugf("Found %d authenticated pubkeys in session store", len(authenticatedPubkeys))

		// Return the first authenticated pubkey we found
		// In a real implementation, we would match this to the specific connection
		return authenticatedPubkeys[0]
	} else {
		logging.Debugf("No authenticated pubkeys found in session store")
	}

	// If we can't determine the authenticated pubkey, return empty string
	return ""
}

// addLogging adds detailed logging for debugging
func addLogging(reqEnvelope *nostr.ReqEnvelope, connPubkey string) {
	logging.Debugf("Authenticated pubkey for filter request: %s", connPubkey)

	// Log the kinds being requested
	for i, filter := range reqEnvelope.Filters {
		logging.Debugf("Filter #%d requests kinds: %v", i+1, filter.Kinds)

		// Log any 'p' tags that might be filtering by pubkey
		for tagName, tagValues := range filter.Tags {
			if tagName == "p" {
				logging.Debugf("Filter #%d requests events for pubkeys: %v", i+1, tagValues)
			}
		}
	}
}

func BuildFilterHandler(store stores.Store) func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
	return func(read lib_nostr.KindReader, write lib_nostr.KindWriter) {
		var json = jsoniter.ConfigCompatibleWithStandardLibrary

		data, err := read()
		if err != nil {
			logging.Infof("Error reading from stream: %s", err)
			write("NOTICE", "Error reading from stream.")
			return
		}

		request, err := decodeRequest(data)
		if err != nil {
			logging.Infof("Error unmarshaling request: %s", err)
			write("NOTICE", "Error unmarshaling request.")
			return
		}

		connPubkey := getAuthenticatedPubkey(data)
		addLogging(&request, connPubkey)
		visibility := newVisibilityContext(store, connPubkey)
		subManager := subscriptionManagerFor(request.Filters)

		var combinedEvents []*nostr.Event
		searchUnavailableNotified := false
		for _, requestFilter := range request.Filters {
			events, queryErr := queryVisibleEvents(store, requestFilter, visibility)
			if queryErr != nil {
				logging.Infof("Error querying events for filter: %v", queryErr)
				if requestFilter.Search != "" && !searchUnavailableNotified {
					write("NOTICE", "NIP-50 search is temporarily unavailable while the relay index is rebuilt.")
					searchUnavailableNotified = true
				}
				continue
			}
			combinedEvents = append(combinedEvents, events...)
		}

		for _, event := range deduplicateEvents(combinedEvents) {
			maybeRefreshSubscription(event, subManager)
			eventJSON, err := json.Marshal(event)
			if err != nil {
				logging.Infof("Error marshaling event: %v", err)
				continue
			}
			write("EVENT", request.SubscriptionID, string(eventJSON))
		}
		write("EOSE", request.SubscriptionID, "End of stored events")
	}
}

func decodeRequest(data []byte) (nostr.ReqEnvelope, error) {
	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	var wrapper struct {
		Request *nostr.ReqEnvelope `json:"request"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Request != nil {
		return *wrapper.Request, nil
	}
	var request nostr.ReqEnvelope
	if err := json.Unmarshal(data, &request); err != nil {
		return request, err
	}
	return request, nil
}

type visibilityContext = eventvisibility.Policy

func newVisibilityContext(store stores.Store, connPubkey string) visibilityContext {
	return eventvisibility.NewPolicy(store, connPubkey, websocket.GetAccessControl())
}

func queryVisibleEvents(store stores.Store, requestFilter nostr.Filter, visibility visibilityContext) ([]*nostr.Event, error) {
	limit := requestFilter.Limit
	if limit <= 0 {
		limit = 500
	}

	parsedSearch := search.ParseSearchQuery(requestFilter.Search)
	includeSpam := parsedSearch.IsSpamIncluded()
	if requestFilter.Search == "" {
		events, err := store.QueryEvents(requestFilter)
		if err != nil {
			return nil, err
		}
		return visibility.FilterBatch(events, includeSpam, limit), nil
	}
	if !store.SearchReady() {
		return nil, fmt.Errorf("NIP-50 search index is not ready")
	}

	pageSize := viper.GetInt("content_filtering.text_filter.search_page_size")
	if pageSize <= 0 {
		pageSize = 128
	}
	maxCandidates := viper.GetInt("content_filtering.text_filter.max_search_candidates")
	if maxCandidates <= 0 {
		maxCandidates = 5000
	}
	if pageSize > maxCandidates {
		pageSize = maxCandidates
	}

	visible := make([]*nostr.Event, 0, limit)
	for offset := 0; offset < maxCandidates && len(visible) < limit; offset += pageSize {
		fetchLimit := pageSize
		if offset+fetchLimit > maxCandidates {
			fetchLimit = maxCandidates - offset
		}
		candidates, err := store.SearchEvents(requestFilter, offset, fetchLimit)
		if err != nil {
			return nil, err
		}
		visible = append(visible, visibility.FilterBatch(candidates, includeSpam, limit-len(visible))...)
		if len(candidates) < fetchLimit {
			break
		}
	}
	return visible, nil
}

func subscriptionManagerFor(filters nostr.Filters) *subscription.SubscriptionManager {
	for _, requestFilter := range filters {
		for _, kind := range requestFilter.Kinds {
			if kind == 11888 {
				return subscription.GetGlobalManager()
			}
		}
	}
	return nil
}

func maybeRefreshSubscription(event *nostr.Event, manager *subscription.SubscriptionManager) {
	if event.Kind != 11888 || manager == nil {
		return
	}
	go func(eventCopy *nostr.Event) {
		if _, err := manager.CheckAndUpdateSubscriptionEvent(eventCopy); err != nil {
			logging.Debugf("Error updating kind 11888 event: %v", err)
		}
	}(event)
}

func deduplicateEvents(events []*nostr.Event) []*nostr.Event {
	seen := make(map[string]struct{}, len(events))
	uniqueEvents := make([]*nostr.Event, 0, len(events))
	for _, event := range events {
		if _, exists := seen[event.ID]; exists {
			continue
		}
		seen[event.ID] = struct{}{}
		uniqueEvents = append(uniqueEvents, event)
	}
	return uniqueEvents
}
