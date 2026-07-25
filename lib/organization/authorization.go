package organization

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/nbd-wtf/go-nostr"
)

const (
	EventKind              = 39504
	InvitationKind         = 39505
	InvitationResponseKind = 39506
	DeletionKind           = 5
)

type Address struct {
	Owner string
	DTag  string
}

func (address Address) String() string {
	return fmt.Sprintf("%d:%s:%s", EventKind, address.Owner, address.DTag)
}

func ParseAddress(value string) (Address, error) {
	parts := strings.SplitN(strings.TrimSpace(value), ":", 3)
	if len(parts) != 3 || parts[0] != strconv.Itoa(EventKind) {
		return Address{}, fmt.Errorf("invalid organization address %q", value)
	}
	owner := strings.ToLower(strings.TrimSpace(parts[1]))
	dTag := strings.TrimSpace(parts[2])
	if !isHexPubkey(owner) || dTag == "" {
		return Address{}, fmt.Errorf("invalid organization address %q", value)
	}
	return Address{Owner: owner, DTag: dTag}, nil
}

func LatestEvent(events []*nostr.Event) *nostr.Event {
	filtered := make([]*nostr.Event, 0, len(events))
	for _, event := range events {
		if event != nil {
			filtered = append(filtered, event)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		leftTime := filtered[i].CreatedAt.Time().Unix()
		rightTime := filtered[j].CreatedAt.Time().Unix()
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return filtered[i].ID < filtered[j].ID
	})
	return filtered[0]
}

func LatestOrganization(store stores.Store, address Address) (*nostr.Event, error) {
	events, err := store.QueryEvents(nostr.Filter{
		Kinds:   []int{EventKind},
		Authors: []string{address.Owner},
		Tags:    nostr.TagMap{"d": []string{address.DTag}},
	})
	if err != nil {
		return nil, fmt.Errorf("query organization: %w", err)
	}
	event := LatestEvent(events)
	if event == nil {
		return nil, fmt.Errorf("organization not found")
	}
	if err := validateOrganizationEvent(event, address); err != nil {
		return nil, err
	}
	deleted, err := eventIsDeleted(store, event)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, fmt.Errorf("organization has been deleted")
	}
	return event, nil
}

func IsMember(store stores.Store, pubkey string, address Address) (bool, error) {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	organizationEvent, err := LatestOrganization(store, address)
	if err != nil {
		return false, err
	}
	if pubkey == address.Owner {
		return true, nil
	}
	if organizationMarksRemoved(organizationEvent, pubkey) {
		return false, nil
	}

	invitations, err := store.QueryEvents(nostr.Filter{
		Kinds:   []int{InvitationKind},
		Authors: []string{address.Owner},
		Tags: nostr.TagMap{
			"a": []string{address.String()},
			"p": []string{pubkey},
		},
	})
	if err != nil {
		return false, fmt.Errorf("query organization invitations: %w", err)
	}

	latestInvitations := make(map[string]*nostr.Event)
	for _, invitation := range invitations {
		invitee, dTag, err := validateInvitationEvent(invitation, address)
		if err != nil || invitee != pubkey {
			continue
		}
		deleted, deletionErr := eventIsDeleted(store, invitation)
		if deletionErr != nil {
			return false, deletionErr
		}
		if deleted {
			continue
		}
		current := latestInvitations[dTag]
		if current == nil || isNewer(invitation, current) {
			latestInvitations[dTag] = invitation
		}
	}

	for invitationDTag, invitation := range latestInvitations {
		responses, queryErr := store.QueryEvents(nostr.Filter{
			Kinds:   []int{InvitationResponseKind},
			Authors: []string{pubkey},
			Tags: nostr.TagMap{
				"a": []string{address.String()},
				"e": []string{invitation.ID},
			},
		})
		if queryErr != nil {
			return false, fmt.Errorf("query organization invitation responses: %w", queryErr)
		}
		expectedDTag := "nosis-org-response-" + invitationDTag
		var latestResponse *nostr.Event
		for _, response := range responses {
			_, responseErr := validateResponseEvent(response, address, invitation, pubkey, expectedDTag)
			if responseErr != nil {
				continue
			}
			deleted, deletionErr := eventIsDeleted(store, response)
			if deletionErr != nil {
				return false, deletionErr
			}
			if deleted {
				continue
			}
			if latestResponse == nil || isNewer(response, latestResponse) {
				latestResponse = response
			}
		}
		if latestResponse != nil {
			status, _ := uniqueTagValue(latestResponse.Tags, "status")
			if strings.EqualFold(strings.TrimSpace(status), "accepted") {
				return true, nil
			}
		}
	}
	return false, nil
}

// ValidateAuthorizedWrite applies organization proof validation even when the
// publisher already has global relay write access. Only a first organization
// definition may omit stored predecessor state; child events still require the
// complete stored proof chain.
func ValidateAuthorizedWrite(event *nostr.Event, store stores.Store) error {
	if event == nil {
		return fmt.Errorf("organization event is required")
	}
	if event.Kind != EventKind {
		return ValidateWriteOverride(event, store)
	}
	dTag, ok := uniqueTagValue(event.Tags, "d")
	if !ok {
		return fmt.Errorf("organization event requires exactly one d tag")
	}
	address := Address{Owner: strings.ToLower(event.PubKey), DTag: strings.TrimSpace(dTag)}
	if err := validateOrganizationEvent(event, address); err != nil {
		return err
	}
	return ensureEventNotDeleted(store, event)
}

func ValidateWriteOverride(event *nostr.Event, store stores.Store) error {
	if event == nil || store == nil {
		return fmt.Errorf("organization event and store are required")
	}
	switch event.Kind {
	case EventKind:
		dTag, ok := uniqueTagValue(event.Tags, "d")
		if !ok {
			return fmt.Errorf("organization event requires exactly one d tag")
		}
		address := Address{Owner: strings.ToLower(event.PubKey), DTag: strings.TrimSpace(dTag)}
		if err := validateOrganizationEvent(event, address); err != nil {
			return err
		}
		if err := ensureEventNotDeleted(store, event); err != nil {
			return err
		}
		if _, err := LatestOrganization(store, address); err != nil {
			return fmt.Errorf("organization update requires an existing valid organization: %w", err)
		}
		return nil
	case InvitationKind:
		addressValue, ok := uniqueTagValue(event.Tags, "a")
		if !ok {
			return fmt.Errorf("organization invitation requires exactly one a tag")
		}
		address, err := ParseAddress(addressValue)
		if err != nil {
			return err
		}
		invitee, _, err := validateInvitationEvent(event, address)
		if err != nil {
			return err
		}
		if err := ensureEventNotDeleted(store, event); err != nil {
			return err
		}
		organizationEvent, err := LatestOrganization(store, address)
		if err != nil {
			return fmt.Errorf("organization invitation requires an existing valid organization: %w", err)
		}
		if organizationMarksRemoved(organizationEvent, invitee) {
			return fmt.Errorf("organization invitation target is marked removed")
		}
		return nil
	case InvitationResponseKind:
		addressValue, ok := uniqueTagValue(event.Tags, "a")
		if !ok {
			return fmt.Errorf("organization response requires exactly one a tag")
		}
		address, err := ParseAddress(addressValue)
		if err != nil {
			return err
		}
		if err := ensureEventNotDeleted(store, event); err != nil {
			return err
		}
		organizationEvent, err := LatestOrganization(store, address)
		if err != nil {
			return fmt.Errorf("organization response requires an existing valid organization: %w", err)
		}
		if organizationMarksRemoved(organizationEvent, strings.ToLower(event.PubKey)) {
			return fmt.Errorf("organization response author is marked removed")
		}
		invitationID, ok := uniqueTagValue(event.Tags, "e")
		if !ok {
			return fmt.Errorf("organization response requires exactly one e tag")
		}
		invitations, err := store.QueryEvents(nostr.Filter{IDs: []string{invitationID}, Kinds: []int{InvitationKind}})
		if err != nil || len(invitations) == 0 {
			return fmt.Errorf("matching organization invitation not found")
		}
		invitee, invitationDTag, err := validateInvitationEvent(invitations[0], address)
		if err != nil {
			return fmt.Errorf("matching organization invitation is invalid: %w", err)
		}
		if invitee != strings.ToLower(event.PubKey) {
			return fmt.Errorf("organization response author does not match invitation target")
		}
		_, err = validateResponseEvent(event, address, invitations[0], invitee, "nosis-org-response-"+invitationDTag)
		return err
	case DeletionKind:
		return validateOrganizationDeletion(event, store)
	default:
		return fmt.Errorf("event kind %d is not an organization authorization event", event.Kind)
	}
}

func validateOrganizationEvent(event *nostr.Event, address Address) error {
	if event == nil || event.Kind != EventKind {
		return fmt.Errorf("invalid organization event kind")
	}
	if strings.ToLower(strings.TrimSpace(event.PubKey)) != address.Owner {
		return fmt.Errorf("organization event author does not match organization owner")
	}
	dTag, ok := uniqueTagValue(event.Tags, "d")
	if !ok || strings.TrimSpace(dTag) != address.DTag {
		return fmt.Errorf("organization event requires exactly one matching d tag")
	}
	return nil
}

func validateInvitationEvent(event *nostr.Event, address Address) (string, string, error) {
	if event == nil || event.Kind != InvitationKind {
		return "", "", fmt.Errorf("invalid organization invitation kind")
	}
	if strings.ToLower(strings.TrimSpace(event.PubKey)) != address.Owner {
		return "", "", fmt.Errorf("organization invitation must be authored by the organization owner")
	}
	aTag, aOK := uniqueTagValue(event.Tags, "a")
	pTag, pOK := uniqueTagValue(event.Tags, "p")
	dTag, dOK := uniqueTagValue(event.Tags, "d")
	invitee := strings.ToLower(strings.TrimSpace(pTag))
	if !aOK || aTag != address.String() || !pOK || !isHexPubkey(invitee) || !dOK || strings.TrimSpace(dTag) == "" {
		return "", "", fmt.Errorf("organization invitation has invalid or duplicate proof tags")
	}
	return invitee, strings.TrimSpace(dTag), nil
}

func validateResponseEvent(event *nostr.Event, address Address, invitation *nostr.Event, invitee string, expectedDTag string) (string, error) {
	if event == nil || event.Kind != InvitationResponseKind {
		return "", fmt.Errorf("invalid organization response kind")
	}
	if strings.ToLower(strings.TrimSpace(event.PubKey)) != invitee {
		return "", fmt.Errorf("organization response author does not match invitation target")
	}
	aTag, aOK := uniqueTagValue(event.Tags, "a")
	eTag, eOK := uniqueTagValue(event.Tags, "e")
	dTag, dOK := uniqueTagValue(event.Tags, "d")
	status, statusOK := uniqueTagValue(event.Tags, "status")
	status = strings.ToLower(strings.TrimSpace(status))
	if !aOK || aTag != address.String() || !eOK || invitation == nil || eTag != invitation.ID || !dOK || dTag != expectedDTag || !statusOK {
		return "", fmt.Errorf("organization response has invalid or duplicate proof tags")
	}
	if status != "accepted" && status != "rejected" {
		return "", fmt.Errorf("organization response status must be accepted or rejected")
	}
	return status, nil
}

func validateOrganizationDeletion(event *nostr.Event, store stores.Store) error {
	addressTag, ok := uniqueTagValue(event.Tags, "a")
	if !ok {
		return fmt.Errorf("organization deletion requires exactly one source a tag")
	}
	parts := strings.SplitN(addressTag, ":", 3)
	if len(parts) != 3 {
		return fmt.Errorf("organization deletion source coordinate is invalid")
	}
	sourceKind, err := strconv.Atoi(parts[0])
	if err != nil || (sourceKind != EventKind && sourceKind != InvitationKind && sourceKind != InvitationResponseKind) {
		return fmt.Errorf("organization deletion source kind is not supported")
	}
	sourceAuthor := strings.ToLower(strings.TrimSpace(parts[1]))
	dTag := strings.TrimSpace(parts[2])
	if sourceAuthor != strings.ToLower(strings.TrimSpace(event.PubKey)) || dTag == "" {
		return fmt.Errorf("organization deletion author does not own the source coordinate")
	}
	kindTag, ok := uniqueTagValue(event.Tags, "k")
	if !ok || kindTag != strconv.Itoa(sourceKind) {
		return fmt.Errorf("organization deletion requires exactly one matching k tag")
	}
	hasEventReference := false
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "e" && strings.TrimSpace(tag[1]) != "" {
			hasEventReference = true
			break
		}
	}
	if !hasEventReference {
		return fmt.Errorf("organization deletion requires at least one source e tag")
	}
	sourceEvents, err := store.QueryEvents(nostr.Filter{
		Kinds:   []int{sourceKind},
		Authors: []string{sourceAuthor},
		Tags:    nostr.TagMap{"d": []string{dTag}},
	})
	if err != nil {
		return fmt.Errorf("query organization deletion source: %w", err)
	}
	source := LatestEvent(sourceEvents)
	if source == nil {
		// A signed author-owned coordinate tombstone is safe to retain before the
		// source arrives and prevents an older event from being resurrected later.
		return nil
	}
	if event.CreatedAt < source.CreatedAt {
		return fmt.Errorf("organization deletion predates the current source event")
	}
	if !hasTagValue(event.Tags, "e", source.ID) {
		return fmt.Errorf("organization deletion does not reference the current source event")
	}
	return nil
}

func ensureEventNotDeleted(store stores.Store, event *nostr.Event) error {
	deleted, err := eventIsDeleted(store, event)
	if err != nil {
		return err
	}
	if deleted {
		return fmt.Errorf("organization authorization event has been deleted")
	}
	return nil
}

func eventIsDeleted(store stores.Store, event *nostr.Event) (bool, error) {
	if store == nil || event == nil {
		return false, fmt.Errorf("organization event and store are required")
	}
	dTag, ok := uniqueTagValue(event.Tags, "d")
	if !ok || strings.TrimSpace(dTag) == "" {
		return false, nil
	}
	coordinate := fmt.Sprintf("%d:%s:%s", event.Kind, strings.ToLower(strings.TrimSpace(event.PubKey)), strings.TrimSpace(dTag))
	deletions, err := store.QueryEvents(nostr.Filter{
		Kinds:   []int{DeletionKind},
		Authors: []string{event.PubKey},
		Tags:    nostr.TagMap{"a": []string{coordinate}},
	})
	if err != nil {
		return false, fmt.Errorf("query organization tombstones: %w", err)
	}
	for _, deletion := range deletions {
		if deletion == nil || deletion.CreatedAt < event.CreatedAt {
			continue
		}
		kindTag, hasKindTag := uniqueTagValue(deletion.Tags, "k")
		if !hasKindTag || kindTag != strconv.Itoa(event.Kind) {
			continue
		}
		return true, nil
	}
	return false, nil
}

func organizationMarksRemoved(event *nostr.Event, pubkey string) bool {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	for _, tag := range event.Tags {
		if len(tag) >= 3 && tag[0] == "p" && strings.ToLower(strings.TrimSpace(tag[1])) == pubkey && strings.EqualFold(strings.TrimSpace(tag[2]), "removed") {
			return true
		}
	}
	return false
}

func uniqueTagValue(tags nostr.Tags, key string) (string, bool) {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			value = tag[1]
			count++
		}
	}
	return value, count == 1
}

func hasTagValue(tags nostr.Tags, key string, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func isNewer(left *nostr.Event, right *nostr.Event) bool {
	if right == nil {
		return left != nil
	}
	if left == nil {
		return false
	}
	leftTime := left.CreatedAt.Time().Unix()
	rightTime := right.CreatedAt.Time().Unix()
	if leftTime != rightTime {
		return leftTime > rightTime
	}
	return left.ID < right.ID
}

func isHexPubkey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
