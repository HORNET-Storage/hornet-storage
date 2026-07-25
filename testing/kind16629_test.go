package testing

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/HORNET-Storage/hornet-storage/testing/helpers"
)

// publishExpectReject publishes an event with a short timeout, expecting it to be rejected
// Returns true if the event was NOT stored (rejection successful)
func publishExpectReject(ctx context.Context, conn *nostr.Relay, event *nostr.Event) bool {
	// Use a short timeout context for the publish - we expect it to fail
	pubCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn.Publish(pubCtx, *event) // Ignore the result - we expect this to fail/timeout

	// Wait a moment for any processing to complete
	time.Sleep(100 * time.Millisecond)

	// Check if the event was stored
	filter := nostr.Filter{IDs: []string{event.ID}}
	events, _ := conn.QuerySync(ctx, filter)

	return len(events) == 0 // True if rejection was successful
}

// buildPermissionTags creates the required tag set for a valid kind 31415 permission event.
// repoAuthor should be the event pubkey for personal repos (clone tag cross-validates this).
// For org repos with an a-tag, pass the org address as both repoAuthor and aTag.
func buildPermissionTags(rTag, repoName, repoAuthor, aTag string, pTags ...nostr.Tag) nostr.Tags {
	cloneURL := fmt.Sprintf("nosis://localhost?id=%s&repo_author=%s&repo_name=%s",
		url.QueryEscape(rTag), url.QueryEscape(repoAuthor), url.QueryEscape(repoName))

	tags := nostr.Tags{
		{"r", rTag},
		{"n", repoName},
		{"clone", cloneURL},
		{"relay", "ws://localhost:9000"},
	}
	if aTag != "" {
		tags = append(tags, nostr.Tag{"a", aTag})
	}
	for _, p := range pTags {
		tags = append(tags, p)
	}
	return tags
}

// ============================================================================
// Test: Tag Validation
// ============================================================================

func TestKind31415_ValidateRTag_RegularRepo(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	tests := []struct {
		name        string
		rTag        string
		expectError bool
	}{
		{
			name:        "Valid r tag with pubkey:reponame",
			rTag:        owner.PublicKey + ":myrepo",
			expectError: false,
		},
		{
			name:        "Invalid r tag - missing reponame",
			rTag:        owner.PublicKey + ":",
			expectError: true,
		},
		{
			name:        "Invalid r tag - missing colon",
			rTag:        owner.PublicKey + "myrepo",
			expectError: true,
		},
		{
			name:        "Invalid r tag - short pubkey",
			rTag:        "abc123:myrepo",
			expectError: true,
		},
		{
			name:        "Invalid r tag - non-hex pubkey",
			rTag:        "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg:myrepo",
			expectError: true,
		},
		{
			name:        "Invalid r tag - empty",
			rTag:        "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tags nostr.Tags
			if tc.rTag == "" {
				// Without r tag, validation should reject for missing r tag
				tags = nostr.Tags{
					{"p", collaborator.PublicKey, "write"},
				}
			} else {
				tags = buildPermissionTags(tc.rTag, "myrepo", owner.PublicKey, "",
					nostr.Tag{"p", collaborator.PublicKey, "write"})
			}

			event, err := helpers.CreateGenericEvent(owner, 31415, "", tags)
			if err != nil {
				t.Fatalf("Failed to create event: %v", err)
			}

			if tc.expectError {
				// For expected rejections, use short timeout and verify event not stored
				rejected := publishExpectReject(ctx, conn, event)
				if !rejected {
					t.Errorf("Expected event to be rejected, but it was stored")
				}
			} else {
				// For expected successes, publish normally
				err = conn.Publish(ctx, *event)
				if err != nil {
					t.Errorf("Expected event to be accepted, but got error: %v", err)
				}
			}
		})
	}
}

func TestKind31415_ValidateRTag_OrgRepo(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	tests := []struct {
		name        string
		rTag        string
		expectError bool
	}{
		{
			name:        "Valid org r tag format",
			rTag:        "39504_" + orgOwner.PublicKey + "_myorg:myrepo",
			expectError: false,
		},
		{
			name:        "Invalid org r tag - missing reponame",
			rTag:        "39504_" + orgOwner.PublicKey + "_myorg:",
			expectError: true,
		},
		{
			name:        "Invalid org r tag - missing dtag",
			rTag:        "39504_" + orgOwner.PublicKey + "_:myrepo",
			expectError: true,
		},
		{
			name:        "Invalid org r tag - short pubkey",
			rTag:        "39504_abc123_myorg:myrepo",
			expectError: true,
		},
		{
			name:        "Invalid org r tag - malformed",
			rTag:        "39504_" + orgOwner.PublicKey + "myrepo",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildPermissionTags(tc.rTag, "myrepo", orgOwner.PublicKey, "",
				nostr.Tag{"p", collaborator.PublicKey, "write"})

			event, err := helpers.CreateGenericEvent(orgOwner, 31415, "", tags)
			if err != nil {
				t.Fatalf("Failed to create event: %v", err)
			}

			if tc.expectError {
				rejected := publishExpectReject(ctx, conn, event)
				if !rejected {
					t.Errorf("Expected event to be rejected, but it was stored")
				}
			} else {
				err = conn.Publish(ctx, *event)
				if err != nil {
					t.Errorf("Expected event to be accepted, but got error: %v", err)
				}
			}
		})
	}
}

func TestKind31415_ValidateATag(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	tests := []struct {
		name        string
		aTag        string
		includeATag bool
		expectError bool
	}{
		{
			name:        "No a tag (valid - optional)",
			aTag:        "",
			includeATag: false,
			expectError: false,
		},
		{
			name:        "Invalid a tag - wrong kind",
			aTag:        "12345:" + owner.PublicKey + ":nosis-organization-test123",
			includeATag: true,
			expectError: true,
		},
		{
			name:        "Invalid a tag - bad pubkey",
			aTag:        "39504:badpubkey:nosis-organization-test123",
			includeATag: true,
			expectError: true,
		},
		{
			name:        "Invalid a tag - short pubkey",
			aTag:        "39504:abc123:nosis-organization-test123",
			includeATag: true,
			expectError: true,
		},
		{
			name:        "Invalid a tag - missing dtag",
			aTag:        "39504:" + owner.PublicKey + ":",
			includeATag: true,
			expectError: true,
		},
		{
			name:        "Invalid a tag - only two parts",
			aTag:        "39504:" + owner.PublicKey,
			includeATag: true,
			expectError: true,
		},
		{
			name:        "Invalid a tag - repo identifier format (not an org address)",
			aTag:        owner.PublicKey + ":myrepo",
			includeATag: true,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
				nostr.Tag{"p", collaborator.PublicKey, "write"})

			if tc.includeATag {
				tags = append(tags, nostr.Tag{"a", tc.aTag})
			}

			event, err := helpers.CreateGenericEvent(owner, 31415, "", tags)
			if err != nil {
				t.Fatalf("Failed to create event: %v", err)
			}

			if tc.expectError {
				rejected := publishExpectReject(ctx, conn, event)
				if !rejected {
					t.Errorf("Expected event to be rejected, but it was stored")
				}
			} else {
				err = conn.Publish(ctx, *event)
				if err != nil {
					t.Errorf("Expected event to be accepted, but got error: %v", err)
				}
			}
		})
	}
}

func TestKind31415_ValidatePermissionTag(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	tests := []struct {
		name            string
		permissionLevel string
		expectError     bool
	}{
		{
			name:            "Valid permission - maintainer",
			permissionLevel: "maintainer",
			expectError:     false,
		},
		{
			name:            "Valid permission - write",
			permissionLevel: "write",
			expectError:     false,
		},
		{
			name:            "Valid permission - triage",
			permissionLevel: "triage",
			expectError:     false,
		},
		{
			name:            "Invalid permission - admin",
			permissionLevel: "admin",
			expectError:     true,
		},
		{
			name:            "Invalid permission - read",
			permissionLevel: "read",
			expectError:     true,
		},
		{
			name:            "Invalid permission - empty",
			permissionLevel: "",
			expectError:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
				nostr.Tag{"p", collaborator.PublicKey, tc.permissionLevel})

			event, err := helpers.CreateGenericEvent(owner, 31415, "", tags)
			if err != nil {
				t.Fatalf("Failed to create event: %v", err)
			}

			if tc.expectError {
				rejected := publishExpectReject(ctx, conn, event)
				if !rejected {
					t.Errorf("Expected event to be rejected, but it was stored")
				}
			} else {
				err = conn.Publish(ctx, *event)
				if err != nil {
					t.Errorf("Expected event to be accepted, but got error: %v", err)
				}
			}
		})
	}
}

func TestKind31415_ValidateRelayTag_DHTPublicKey(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()
	dhtKey := "f51a2356dd4aa7678658adaf659a8b43b51a94075b1fed254fc39d64211bf25d"

	tests := []struct {
		name        string
		relayTag    string
		expectError bool
	}{
		{
			name:        "Valid raw DHT public key",
			relayTag:    dhtKey,
			expectError: false,
		},
		{
			name:        "Valid nosis DHT public key",
			relayTag:    "nosis://" + dhtKey,
			expectError: false,
		},
		{
			name:        "Invalid nosis DHT public key",
			relayTag:    "nosis://abc123",
			expectError: true,
		},
	}

	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoName := fmt.Sprintf("myrepo-%d", index)
			rTag := owner.PublicKey + ":" + repoName
			tags := buildPermissionTags(rTag, repoName, owner.PublicKey, "",
				nostr.Tag{"p", collaborator.PublicKey, "write"})

			for i, tag := range tags {
				if len(tag) >= 2 && tag[0] == "relay" {
					tags[i][1] = tc.relayTag
				}
			}

			event, err := helpers.CreateGenericEvent(owner, 31415, "", tags)
			if err != nil {
				t.Fatalf("Failed to create event: %v", err)
			}

			if tc.expectError {
				rejected := publishExpectReject(ctx, conn, event)
				if !rejected {
					t.Errorf("Expected event to be rejected, but it was stored")
				}
			} else {
				err = conn.Publish(ctx, *event)
				if err != nil {
					t.Errorf("Expected event to be accepted, but got error: %v", err)
				}
			}
		})
	}
}

// ============================================================================
// Test: Regular Repo Permissions
// ============================================================================

func TestKind31415_RegularRepo_OwnerCanCreate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"
	tags := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})

	event, err := helpers.CreateGenericEvent(owner, 31415, "", tags)
	if err != nil {
		t.Fatalf("Failed to create event: %v", err)
	}

	err = conn.Publish(ctx, *event)
	if err != nil {
		t.Fatalf("Owner should be able to create permission event: %v", err)
	}

	// Verify event was stored
	filter := nostr.Filter{IDs: []string{event.ID}}
	events, err := conn.QuerySync(ctx, filter)
	if err != nil {
		t.Fatalf("Failed to query events: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
}

func TestKind31415_RegularRepo_NonOwnerCannotCreate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	attacker, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	// r tag identifies owner's repo
	rTag := owner.PublicKey + ":myrepo"
	tags := buildPermissionTags(rTag, "myrepo", attacker.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})

	// Attacker tries to create permission event for owner's repo
	event, err := helpers.CreateGenericEvent(attacker, 31415, "", tags)
	if err != nil {
		t.Fatalf("Failed to create event: %v", err)
	}

	// Use publishExpectReject for expected rejection
	rejected := publishExpectReject(ctx, conn, event)
	if !rejected {
		t.Errorf("Non-owner should not be able to create permission event for another user's repo")
	}
}

func TestKind31415_RegularRepo_OwnerCanUpdate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator1, _ := helpers.GenerateKeyPair()
	collaborator2, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	// Create first permission event
	tags1 := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator1.PublicKey, "write"})
	event1, _ := helpers.CreateGenericEvent(owner, 31415, "", tags1)
	err = conn.Publish(ctx, *event1)
	if err != nil {
		t.Fatalf("Failed to create first permission event: %v", err)
	}

	// Wait a moment to ensure different timestamps
	time.Sleep(100 * time.Millisecond)

	// Update with new permissions
	tags2 := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator1.PublicKey, "maintainer"},
		nostr.Tag{"p", collaborator2.PublicKey, "write"})
	event2, _ := helpers.CreateGenericEvent(owner, 31415, "", tags2)
	err = conn.Publish(ctx, *event2)
	if err != nil {
		t.Fatalf("Owner should be able to update permission event: %v", err)
	}

	// Verify only the new event exists
	filter := nostr.Filter{
		Kinds: []int{31415},
		Tags: nostr.TagMap{
			"r": []string{rTag},
		},
	}
	events, err := conn.QuerySync(ctx, filter)
	if err != nil {
		t.Fatalf("Failed to query events: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 event after replacement, got %d", len(events))
	}

	if len(events) > 0 && events[0].ID != event2.ID {
		t.Errorf("Expected new event ID %s, got %s", event2.ID, events[0].ID)
	}
}

func TestKind31415_RegularRepo_NonOwnerCannotUpdate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	attacker, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	// Owner creates initial permission event
	tags1 := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	event1, _ := helpers.CreateGenericEvent(owner, 31415, "", tags1)
	err = conn.Publish(ctx, *event1)
	if err != nil {
		t.Fatalf("Failed to create initial permission event: %v", err)
	}

	// Verify the initial event was stored
	initialFilter := nostr.Filter{IDs: []string{event1.ID}}
	initialEvents, err := conn.QuerySync(ctx, initialFilter)
	if err != nil || len(initialEvents) != 1 {
		t.Fatalf("Initial event was not stored properly: got %d events, err: %v", len(initialEvents), err)
	}

	// Attacker tries to update (we don't wait for this, it will be rejected with NOTICE)
	tags2 := buildPermissionTags(rTag, "myrepo", attacker.PublicKey, "",
		nostr.Tag{"p", attacker.PublicKey, "maintainer"})
	event2, _ := helpers.CreateGenericEvent(attacker, 31415, "", tags2)

	// Use a short timeout context for the attacker's publish - we expect it to fail
	attackCtx, attackCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer attackCancel()
	conn.Publish(attackCtx, *event2) // Ignore the result - we expect this to fail/timeout

	// Wait a moment for any processing to complete
	time.Sleep(200 * time.Millisecond)

	// Verify original event still exists and attacker's event was rejected
	filter := nostr.Filter{
		Kinds: []int{31415},
		Tags: nostr.TagMap{
			"r": []string{rTag},
		},
	}
	events, _ := conn.QuerySync(ctx, filter)

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}

	if len(events) > 0 && events[0].ID != event1.ID {
		t.Errorf("Original event should still exist, expected %s got %s", event1.ID, events[0].ID)
	}
}

// ============================================================================
// Test: Org Repo Permissions
// ============================================================================

func TestKind31415_OrgRepo_OwnerCanCreate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	// First create the org event (kind 39504)
	orgDtag := "myorg"
	orgTags := nostr.Tags{
		{"d", orgDtag},
	}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	err = conn.Publish(ctx, *orgEvent)
	if err != nil {
		t.Fatalf("Failed to create org event: %v", err)
	}

	// Create permission event for org repo
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":myrepo"
	tags := buildPermissionTags(rTag, "myrepo", orgOwner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})

	event, _ := helpers.CreateGenericEvent(orgOwner, 31415, "", tags)
	err = conn.Publish(ctx, *event)
	if err != nil {
		t.Fatalf("Org owner should be able to create permission event: %v", err)
	}

	// Verify event was stored
	filter := nostr.Filter{IDs: []string{event.ID}}
	events, _ := conn.QuerySync(ctx, filter)
	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
}

func TestKind31415_OrgRepo_MemberCanCreateFirst(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	member, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	orgDtag := "myorg"
	orgAddress := "39504:" + orgOwner.PublicKey + ":" + orgDtag

	// Create org event
	orgTags := nostr.Tags{{"d", orgDtag}}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	conn.Publish(ctx, *orgEvent)

	// Create invitation for member
	inviteDTag := "nosis-org-invite-member-can-create"
	inviteTags := nostr.Tags{
		{"d", inviteDTag},
		{"a", orgAddress},
		{"p", member.PublicKey},
		{"role", "developer"},
	}
	inviteEvent, _ := helpers.CreateGenericEvent(orgOwner, 39505, "", inviteTags)
	conn.Publish(ctx, *inviteEvent)

	// Member accepts invitation
	acceptTags := nostr.Tags{
		{"d", "nosis-org-response-" + inviteDTag},
		{"e", inviteEvent.ID},
		{"status", "accepted"},
		{"a", orgAddress},
	}
	acceptEvent, _ := helpers.CreateGenericEvent(member, 39506, "", acceptTags)
	conn.Publish(ctx, *acceptEvent)

	// Member creates permission event for org repo
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":newrepo"
	permTags := buildPermissionTags(rTag, "newrepo", member.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	permEvent, _ := helpers.CreateGenericEvent(member, 31415, "", permTags)
	err = conn.Publish(ctx, *permEvent)
	if err != nil {
		t.Fatalf("Verified org member should be able to create first permission event: %v", err)
	}

	// Verify event was stored
	filter := nostr.Filter{IDs: []string{permEvent.ID}}
	events, _ := conn.QuerySync(ctx, filter)
	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
}

func TestKind31415_OrgRepo_NonMemberCannotCreate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	nonMember, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	orgDtag := "myorg"

	// Create org event
	orgTags := nostr.Tags{{"d", orgDtag}}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	conn.Publish(ctx, *orgEvent)

	// Non-member tries to create permission event
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":myrepo"
	tags := buildPermissionTags(rTag, "myrepo", nonMember.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	event, _ := helpers.CreateGenericEvent(nonMember, 31415, "", tags)

	// Use publishExpectReject for expected rejection
	rejected := publishExpectReject(ctx, conn, event)
	if !rejected {
		t.Errorf("Non-member should not be able to create permission event for org repo")
	}
}

func TestKind31415_OrgRepo_OnlyOwnerCanUpdate(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	member, _ := helpers.GenerateKeyPair()
	collaborator1, _ := helpers.GenerateKeyPair()
	collaborator2, _ := helpers.GenerateKeyPair()

	orgDtag := "myorg"
	orgAddress := "39504:" + orgOwner.PublicKey + ":" + orgDtag

	// Create org event
	orgTags := nostr.Tags{{"d", orgDtag}}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	conn.Publish(ctx, *orgEvent)

	// Create and accept invitation for member
	inviteDTag := "nosis-org-invite-owner-update"
	inviteTags := nostr.Tags{
		{"d", inviteDTag},
		{"a", orgAddress},
		{"p", member.PublicKey},
	}
	inviteEvent, _ := helpers.CreateGenericEvent(orgOwner, 39505, "", inviteTags)
	conn.Publish(ctx, *inviteEvent)

	acceptTags := nostr.Tags{
		{"d", "nosis-org-response-" + inviteDTag},
		{"e", inviteEvent.ID},
		{"status", "accepted"},
		{"a", orgAddress},
	}
	acceptEvent, _ := helpers.CreateGenericEvent(member, 39506, "", acceptTags)
	conn.Publish(ctx, *acceptEvent)

	// Member creates first permission event
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":myrepo"
	tags1 := buildPermissionTags(rTag, "myrepo", member.PublicKey, "",
		nostr.Tag{"p", collaborator1.PublicKey, "write"})
	event1, _ := helpers.CreateGenericEvent(member, 31415, "", tags1)
	err = conn.Publish(ctx, *event1)
	if err != nil {
		t.Fatalf("Member should be able to create first event: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Member tries to update (should fail - only owner can update)
	tags2 := buildPermissionTags(rTag, "myrepo", member.PublicKey, "",
		nostr.Tag{"p", collaborator2.PublicKey, "write"})
	event2, _ := helpers.CreateGenericEvent(member, 31415, "", tags2)
	rejected := publishExpectReject(ctx, conn, event2)
	if !rejected {
		t.Errorf("Member should not be able to update permission event (only org owner can)")
	}

	// Verify original event still exists
	filter := nostr.Filter{
		Kinds: []int{31415},
		Tags: nostr.TagMap{
			"r": []string{rTag},
		},
	}
	events, _ := conn.QuerySync(ctx, filter)

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}

	if len(events) > 0 && events[0].ID != event1.ID {
		t.Errorf("Original event should still exist, expected %s got %s", event1.ID, events[0].ID)
	}

	// Org owner updates (should succeed)
	time.Sleep(100 * time.Millisecond)
	tags3 := buildPermissionTags(rTag, "myrepo", orgOwner.PublicKey, "",
		nostr.Tag{"p", collaborator1.PublicKey, "maintainer"},
		nostr.Tag{"p", collaborator2.PublicKey, "write"})
	event3, _ := helpers.CreateGenericEvent(orgOwner, 31415, "", tags3)
	err = conn.Publish(ctx, *event3)
	if err != nil {
		t.Fatalf("Org owner should be able to update permission event: %v", err)
	}

	// Verify owner's update succeeded
	events, _ = conn.QuerySync(ctx, filter)
	if len(events) != 1 {
		t.Errorf("Expected 1 event after owner update, got %d", len(events))
	}

	if len(events) > 0 && events[0].ID != event3.ID {
		t.Errorf("Owner's event should replace member's, expected %s got %s", event3.ID, events[0].ID)
	}
}

// ============================================================================
// Test: Invitation/Membership Verification
// ============================================================================

func TestKind31415_OrgRepo_DeletedInvitationInvalidatesMembership(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	member, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	orgDtag := "myorg"
	orgAddress := "39504:" + orgOwner.PublicKey + ":" + orgDtag

	// Create org
	orgTags := nostr.Tags{{"d", orgDtag}}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	conn.Publish(ctx, *orgEvent)

	// Create invitation
	inviteDTag := "nosis-org-invite-deleted"
	inviteTags := nostr.Tags{
		{"d", inviteDTag},
		{"a", orgAddress},
		{"p", member.PublicKey},
	}
	inviteEvent, _ := helpers.CreateGenericEvent(orgOwner, 39505, "", inviteTags)
	conn.Publish(ctx, *inviteEvent)

	// Member accepts
	acceptTags := nostr.Tags{
		{"d", "nosis-org-response-" + inviteDTag},
		{"e", inviteEvent.ID},
		{"status", "accepted"},
		{"a", orgAddress},
	}
	acceptEvent, _ := helpers.CreateGenericEvent(member, 39506, "", acceptTags)
	conn.Publish(ctx, *acceptEvent)

	// Org owner deletes the invitation (kind 5)
	deleteTags := nostr.Tags{
		{"e", inviteEvent.ID},
		{"a", fmt.Sprintf("39505:%s:%s", orgOwner.PublicKey, inviteDTag)},
		{"k", "39505"},
	}
	deleteEvent, _ := helpers.CreateGenericEvent(orgOwner, 5, "Removed from org", deleteTags)
	conn.Publish(ctx, *deleteEvent)

	// Member tries to create permission event (should fail - invitation deleted)
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":newrepo"
	permTags := buildPermissionTags(rTag, "newrepo", member.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	permEvent, _ := helpers.CreateGenericEvent(member, 31415, "", permTags)

	// Use publishExpectReject for expected rejection
	rejected := publishExpectReject(ctx, conn, permEvent)
	if !rejected {
		t.Errorf("Member with deleted invitation should not be able to create permission event")
	}
}

func TestKind31415_OrgRepo_PendingInvitationNotValid(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	orgOwner, _ := helpers.GenerateKeyPair()
	invitedUser, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	orgDtag := "myorg"
	orgAddress := "39504:" + orgOwner.PublicKey + ":" + orgDtag

	// Create org
	orgTags := nostr.Tags{{"d", orgDtag}}
	orgEvent, _ := helpers.CreateGenericEvent(orgOwner, 39504, `{"name":"My Org"}`, orgTags)
	conn.Publish(ctx, *orgEvent)

	// Create invitation (but don't accept it)
	inviteTags := nostr.Tags{
		{"d", "nosis-org-invite-pending"},
		{"a", orgAddress},
		{"p", invitedUser.PublicKey},
	}
	inviteEvent, _ := helpers.CreateGenericEvent(orgOwner, 39505, "", inviteTags)
	conn.Publish(ctx, *inviteEvent)

	// Invited user tries to create permission event without accepting
	rTag := "39504_" + orgOwner.PublicKey + "_" + orgDtag + ":myrepo"
	permTags := buildPermissionTags(rTag, "myrepo", invitedUser.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	permEvent, _ := helpers.CreateGenericEvent(invitedUser, 31415, "", permTags)

	// Use publishExpectReject for expected rejection
	rejected := publishExpectReject(ctx, conn, permEvent)
	if !rejected {
		t.Errorf("User with pending (not accepted) invitation should not be able to create permission event")
	}
}

// ============================================================================
// Test: Event Replacement
// ============================================================================

func TestKind31415_ReplacementDeletesOldEvent(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator1, _ := helpers.GenerateKeyPair()
	collaborator2, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	// Create first event
	tags1 := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator1.PublicKey, "write"})
	event1, _ := helpers.CreateGenericEvent(owner, 31415, "", tags1)
	conn.Publish(ctx, *event1)

	time.Sleep(100 * time.Millisecond)

	// Create second event (replacement)
	tags2 := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
		nostr.Tag{"p", collaborator2.PublicKey, "maintainer"})
	event2, _ := helpers.CreateGenericEvent(owner, 31415, "", tags2)
	conn.Publish(ctx, *event2)

	// Query by r tag - should only get one event
	filter := nostr.Filter{
		Kinds: []int{31415},
		Tags: nostr.TagMap{
			"r": []string{rTag},
		},
	}
	events, _ := conn.QuerySync(ctx, filter)

	if len(events) != 1 {
		t.Errorf("Expected 1 event (old should be deleted), got %d", len(events))
	}

	// Query for old event by ID - should not exist
	oldFilter := nostr.Filter{IDs: []string{event1.ID}}
	oldEvents, _ := conn.QuerySync(ctx, oldFilter)

	if len(oldEvents) > 0 {
		t.Errorf("Old event should be deleted after replacement")
	}
}

func TestKind31415_MultipleReplacementsOnlyKeepsLatest(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag := owner.PublicKey + ":myrepo"

	var lastEventID string
	// Create multiple events
	for i := 0; i < 5; i++ {
		tags := buildPermissionTags(rTag, "myrepo", owner.PublicKey, "",
			nostr.Tag{"p", collaborator.PublicKey, "write"})
		event, _ := helpers.CreateGenericEvent(owner, 31415, "", tags)
		conn.Publish(ctx, *event)
		lastEventID = event.ID
		time.Sleep(50 * time.Millisecond)
	}

	// Query by r tag - should only get one event
	filter := nostr.Filter{
		Kinds: []int{31415},
		Tags: nostr.TagMap{
			"r": []string{rTag},
		},
	}
	events, _ := conn.QuerySync(ctx, filter)

	if len(events) != 1 {
		t.Errorf("Expected 1 event after 5 replacements, got %d", len(events))
	}

	if len(events) > 0 && events[0].ID != lastEventID {
		t.Errorf("Expected latest event ID %s, got %s", lastEventID, events[0].ID)
	}
}

// ============================================================================
// Test: Different Repos Same Owner
// ============================================================================

func TestKind31415_DifferentReposSameOwner(t *testing.T) {
	relay := setupKind31415TestRelay(t)
	defer relay.Cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := relay.Connect(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer conn.Close()

	owner, _ := helpers.GenerateKeyPair()
	collaborator, _ := helpers.GenerateKeyPair()

	rTag1 := owner.PublicKey + ":repo1"
	rTag2 := owner.PublicKey + ":repo2"

	// Create permission for repo1
	tags1 := buildPermissionTags(rTag1, "repo1", owner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "write"})
	event1, _ := helpers.CreateGenericEvent(owner, 31415, "", tags1)
	conn.Publish(ctx, *event1)

	// Create permission for repo2
	tags2 := buildPermissionTags(rTag2, "repo2", owner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "maintainer"})
	event2, _ := helpers.CreateGenericEvent(owner, 31415, "", tags2)
	conn.Publish(ctx, *event2)

	// Query for repo1
	filter1 := nostr.Filter{
		Kinds: []int{31415},
		Tags:  nostr.TagMap{"r": []string{rTag1}},
	}
	events1, _ := conn.QuerySync(ctx, filter1)

	// Query for repo2
	filter2 := nostr.Filter{
		Kinds: []int{31415},
		Tags:  nostr.TagMap{"r": []string{rTag2}},
	}
	events2, _ := conn.QuerySync(ctx, filter2)

	if len(events1) != 1 {
		t.Errorf("Expected 1 event for repo1, got %d", len(events1))
	}

	if len(events2) != 1 {
		t.Errorf("Expected 1 event for repo2, got %d", len(events2))
	}

	// Updating repo1 should not affect repo2
	time.Sleep(50 * time.Millisecond)
	tags1Updated := buildPermissionTags(rTag1, "repo1", owner.PublicKey, "",
		nostr.Tag{"p", collaborator.PublicKey, "triage"})
	event1Updated, _ := helpers.CreateGenericEvent(owner, 31415, "", tags1Updated)
	conn.Publish(ctx, *event1Updated)

	events1, _ = conn.QuerySync(ctx, filter1)
	events2, _ = conn.QuerySync(ctx, filter2)

	if len(events1) != 1 || events1[0].ID != event1Updated.ID {
		t.Errorf("Repo1 should have updated event")
	}

	if len(events2) != 1 || events2[0].ID != event2.ID {
		t.Errorf("Repo2 event should be unchanged")
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

// setupKind31415TestRelay creates a test relay for kind31415 tests
func setupKind31415TestRelay(t *testing.T) *helpers.TestRelay {
	t.Helper()
	cfg := helpers.DefaultTestConfig()
	// Include all kinds we need for these tests
	cfg.AllowedKinds = []int{0, 1, 3, 5, 31415, 39504, 39505, 39506}
	relay, err := helpers.NewTestRelay(cfg)
	if err != nil {
		t.Fatalf("Failed to create test relay: %v", err)
	}
	return relay
}
