package push

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/statistics"
	"github.com/nbd-wtf/go-nostr"
)

// mockPushStore implements stores.Store for push notification payload tests.
// Only QueryEvents and GetStatsStore are implemented; all other methods
// are satisfied by embedding the interface (will panic if called).
type mockPushStore struct {
	stores.Store // satisfies full interface; unimplemented methods panic if called
	events       []*nostr.Event
}

func (m *mockPushStore) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	var results []*nostr.Event
	for _, event := range m.events {
		// Match by IDs
		if len(filter.IDs) > 0 {
			for _, id := range filter.IDs {
				if event.ID == id {
					results = append(results, event)
				}
			}
			continue
		}
		// Match by Authors + Kinds
		matchAuthor := len(filter.Authors) == 0
		matchKind := len(filter.Kinds) == 0
		for _, author := range filter.Authors {
			if event.PubKey == author {
				matchAuthor = true
				break
			}
		}
		for _, kind := range filter.Kinds {
			if event.Kind == kind {
				matchKind = true
				break
			}
		}
		if matchAuthor && matchKind {
			results = append(results, event)
		}
	}
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}
	return results, nil
}

func (m *mockPushStore) GetStatsStore() statistics.StatisticsStore {
	return nil
}

// newTestPushService creates a PushService with a mock store for testing
// newTestPushService creates a PushService with a mock store for testing
func newTestPushService(events []*nostr.Event) *PushService {
	return &PushService{
		store:          &mockPushStore{events: events},
		nameCache:      make(map[string]string),
		followCache:    make(map[string]*followCacheEntry),
		followCacheTTL: 5 * time.Minute,
		followCacheMax: 500,
		followGated:    false, // disabled by default in tests
	}
}

// TestPayload_ReactionLike tests that a kind 7 "+" reaction produces the correct APNs payload
// including referenced event info (ID, kind, content snippet).
func TestPayload_ReactionLike(t *testing.T) {
	// Original note that will be liked
	originalNote := &nostr.Event{
		ID:        "original_note_id_123",
		PubKey:    "recipient_pubkey_abc",
		Kind:      1,
		Content:   "This is the original note that someone will like",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	// Reactor's profile so the name resolves to "Mo"
	reactorProfile := &nostr.Event{
		ID:        "reactor_profile_id",
		PubKey:    "reactor_pubkey_xyz",
		Kind:      0,
		Content:   `{"name":"Mo","display_name":"Mo","about":"Test user"}`,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	// Kind 7 reaction (like) referencing the original note
	reactionEvent := &nostr.Event{
		ID:        "reaction_event_id_456",
		PubKey:    "reactor_pubkey_xyz",
		Kind:      7,
		Content:   "+",
		Tags:      nostr.Tags{{"e", "original_note_id_123"}, {"p", "recipient_pubkey_abc"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalNote, reactorProfile})
	message := ps.formatNotificationMessage(reactionEvent, "recipient_pubkey_abc")

	// Verify PushMessage fields
	assertEqual(t, "Title", "New Reaction", message.Title)
	assertEqual(t, "Body", "Mo liked your note", message.Body)
	assertEqual(t, "Badge", 1, message.Badge)
	assertEqual(t, "Sound", "default", message.Sound)
	assertEqual(t, "Category", "kind_7", message.Category)
	if !message.MutableContent {
		t.Error("Expected MutableContent to be true")
	}

	// Verify base event data
	assertEqual(t, "Data.event_id", "reaction_event_id_456", message.Data["event_id"])
	assertEqual(t, "Data.event_kind", 7, message.Data["event_kind"])
	assertEqual(t, "Data.pubkey", "reactor_pubkey_xyz", message.Data["pubkey"])

	// Verify referenced event info (the NEW fields)
	assertEqual(t, "Data.referenced_event_id", "original_note_id_123", message.Data["referenced_event_id"])
	assertEqual(t, "Data.referenced_event_kind", 1, message.Data["referenced_event_kind"])
	assertEqual(t, "Data.referenced_event_content",
		"This is the original note that someone will like",
		message.Data["referenced_event_content"])

	// Log the final APNs payload
	logPayload(t, "Kind 7 (Reaction/Like)", message)
}

// TestPayload_EmojiReaction tests that emoji reactions show the emoji in the body.
func TestPayload_EmojiReaction(t *testing.T) {
	originalNote := &nostr.Event{
		ID:        "note_id_emoji_test",
		PubKey:    "author_pubkey",
		Kind:      1,
		Content:   "A wonderful post worth reacting to",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reactorProfile := &nostr.Event{
		ID:        "reactor_profile",
		PubKey:    "reactor_pubkey",
		Kind:      0,
		Content:   `{"name":"Bob"}`,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reactionEvent := &nostr.Event{
		ID:        "emoji_reaction_id",
		PubKey:    "reactor_pubkey",
		Kind:      7,
		Content:   "🔥",
		Tags:      nostr.Tags{{"e", "note_id_emoji_test"}, {"p", "author_pubkey"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalNote, reactorProfile})
	message := ps.formatNotificationMessage(reactionEvent, "author_pubkey")

	assertEqual(t, "Title", "New Reaction", message.Title)
	assertEqual(t, "Body", "Bob reacted 🔥 to your note", message.Body)
	assertEqual(t, "Data.referenced_event_id", "note_id_emoji_test", message.Data["referenced_event_id"])
	assertEqual(t, "Data.referenced_event_content", "A wonderful post worth reacting to", message.Data["referenced_event_content"])

	logPayload(t, "Kind 7 (Emoji Reaction 🔥)", message)
}

// TestPayload_Repost tests that a kind 6 repost includes referenced event info.
func TestPayload_Repost(t *testing.T) {
	originalNote := &nostr.Event{
		ID:        "original_note_repost",
		PubKey:    "original_author_pubkey",
		Kind:      1,
		Content:   "Breaking news from the Nostr network",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reposterProfile := &nostr.Event{
		ID:        "reposter_profile_id",
		PubKey:    "reposter_pubkey",
		Kind:      0,
		Content:   `{"display_name":"Charlie"}`,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	repostEvent := &nostr.Event{
		ID:        "repost_event_id",
		PubKey:    "reposter_pubkey",
		Kind:      6,
		Content:   "",
		Tags:      nostr.Tags{{"e", "original_note_repost"}, {"p", "original_author_pubkey"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalNote, reposterProfile})
	message := ps.formatNotificationMessage(repostEvent, "original_author_pubkey")

	assertEqual(t, "Title", "Repost", message.Title)
	assertEqual(t, "Body", "Charlie reposted your note", message.Body)
	assertEqual(t, "Category", "kind_6", message.Category)
	assertEqual(t, "Data.referenced_event_id", "original_note_repost", message.Data["referenced_event_id"])
	assertEqual(t, "Data.referenced_event_kind", 1, message.Data["referenced_event_kind"])
	assertEqual(t, "Data.referenced_event_content", "Breaking news from the Nostr network", message.Data["referenced_event_content"])

	logPayload(t, "Kind 6 (Repost)", message)
}

// TestPayload_AudioRepost tests that a kind 1809 audio repost includes referenced event info.
func TestPayload_AudioRepost(t *testing.T) {
	originalAudioNote := &nostr.Event{
		ID:        "original_audio_id_789",
		PubKey:    "recipient_pubkey_abc",
		Kind:      1808,
		Content:   "Check out my new audio track!",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reposterProfile := &nostr.Event{
		ID:        "reposter_profile_id",
		PubKey:    "reposter_pubkey_def",
		Kind:      0,
		Content:   `{"name":"Alice","display_name":"Alice","about":"Music lover"}`,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	repostEvent := &nostr.Event{
		ID:        "repost_event_id_012",
		PubKey:    "reposter_pubkey_def",
		Kind:      1809,
		Content:   "",
		Tags:      nostr.Tags{{"e", "original_audio_id_789"}, {"p", "recipient_pubkey_abc"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalAudioNote, reposterProfile})
	message := ps.formatNotificationMessage(repostEvent, "recipient_pubkey_abc")

	assertEqual(t, "Title", "Audio Repost", message.Title)
	assertEqual(t, "Body", "Alice reposted your audio post", message.Body)
	assertEqual(t, "Category", "kind_1809", message.Category)
	assertEqual(t, "Data.referenced_event_id", "original_audio_id_789", message.Data["referenced_event_id"])
	assertEqual(t, "Data.referenced_event_kind", 1808, message.Data["referenced_event_kind"])
	assertEqual(t, "Data.referenced_event_content", "Check out my new audio track!", message.Data["referenced_event_content"])

	logPayload(t, "Kind 1809 (Audio Repost)", message)
}

// TestPayload_ContentTruncation verifies that long referenced content is truncated to 100 chars.
func TestPayload_ContentTruncation(t *testing.T) {
	longContent := ""
	for i := 0; i < 20; i++ {
		longContent += "This is a very long note. "
	}

	originalNote := &nostr.Event{
		ID:        "long_note_id",
		PubKey:    "author_pubkey",
		Kind:      1,
		Content:   longContent,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reactionEvent := &nostr.Event{
		ID:        "reaction_to_long",
		PubKey:    "reactor_pubkey",
		Kind:      7,
		Content:   "+",
		Tags:      nostr.Tags{{"e", "long_note_id"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalNote})
	message := ps.formatNotificationMessage(reactionEvent, "author_pubkey")

	refContent, ok := message.Data["referenced_event_content"].(string)
	if !ok {
		t.Fatal("referenced_event_content not found or not a string")
	}
	maxLen := 103 // 100 chars + "..."
	if len(refContent) > maxLen {
		t.Errorf("Content should be truncated to ~103 chars, got length %d", len(refContent))
	}
	if refContent[len(refContent)-3:] != "..." {
		t.Errorf("Truncated content should end with '...', got suffix '%s'", refContent[len(refContent)-3:])
	}

	t.Logf("Truncated content (%d chars): %s", len(refContent), refContent)
}

// TestPayload_MissingReferencedEvent verifies graceful handling when the referenced event
// is not found in the store (e.g., event came from another relay).
func TestPayload_MissingReferencedEvent(t *testing.T) {
	reactionEvent := &nostr.Event{
		ID:        "reaction_to_missing",
		PubKey:    "reactor_pubkey",
		Kind:      7,
		Content:   "+",
		Tags:      nostr.Tags{{"e", "nonexistent_event_id"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{}) // empty store
	message := ps.formatNotificationMessage(reactionEvent, "author_pubkey")

	// Should still have basic notification fields
	assertEqual(t, "Title", "New Reaction", message.Title)

	// referenced_event_id should ALWAYS be present — it comes from the e-tag on the event itself
	refID, exists := message.Data["referenced_event_id"]
	if !exists {
		t.Error("referenced_event_id should always be present when event has an e-tag")
	}
	assertEqual(t, "referenced_event_id", "nonexistent_event_id", refID)

	// Kind and content should NOT be present (they require DB lookup which found nothing)
	if _, exists := message.Data["referenced_event_kind"]; exists {
		t.Error("Should not have referenced_event_kind when referenced event is not in DB")
	}
	if _, exists := message.Data["referenced_event_content"]; exists {
		t.Error("Should not have referenced_event_content when referenced event is not in DB")
	}

	logPayload(t, "Kind 7 (Missing Referenced Event)", message)
}

// TestPayload_APNsJSONStructure verifies the final JSON structure matches APNs requirements.
func TestPayload_APNsJSONStructure(t *testing.T) {
	originalNote := &nostr.Event{
		ID:        "struct_test_note",
		PubKey:    "note_author",
		Kind:      1,
		Content:   "Hello world",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reactorProfile := &nostr.Event{
		ID:        "struct_test_profile",
		PubKey:    "reactor",
		Kind:      0,
		Content:   `{"name":"TestUser"}`,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	reactionEvent := &nostr.Event{
		ID:        "struct_test_reaction",
		PubKey:    "reactor",
		Kind:      7,
		Content:   "+",
		Tags:      nostr.Tags{{"e", "struct_test_note"}, {"p", "note_author"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{originalNote, reactorProfile})
	message := ps.formatNotificationMessage(reactionEvent, "note_author")
	payload := message.ToAPNsPayload()

	// Marshal to JSON (this is what APNs actually receives)
	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal payload to JSON: %v", err)
	}

	// Parse it back to verify structure
	var parsed map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal payload JSON: %v", err)
	}

	// Verify top-level keys
	requiredKeys := []string{"aps", "event_id", "event_kind", "pubkey", "referenced_event_id", "referenced_event_kind", "referenced_event_content"}
	for _, key := range requiredKeys {
		if _, exists := parsed[key]; !exists {
			t.Errorf("Missing required top-level key: %s", key)
		}
	}

	// Verify aps structure
	aps, ok := parsed["aps"].(map[string]interface{})
	if !ok {
		t.Fatal("'aps' is not a JSON object")
	}

	apsKeys := []string{"alert", "badge", "sound", "mutable-content", "category"}
	for _, key := range apsKeys {
		if _, exists := aps[key]; !exists {
			t.Errorf("Missing aps key: %s", key)
		}
	}

	// Verify alert structure
	alert, ok := aps["alert"].(map[string]interface{})
	if !ok {
		t.Fatal("'aps.alert' is not a JSON object")
	}
	if _, exists := alert["title"]; !exists {
		t.Error("Missing alert.title")
	}
	if _, exists := alert["body"]; !exists {
		t.Error("Missing alert.body")
	}

	// Verify mutable-content is 1 (required for iOS notification service extension)
	if mc, ok := aps["mutable-content"].(float64); !ok || mc != 1 {
		t.Errorf("Expected mutable-content=1, got %v", aps["mutable-content"])
	}

	t.Logf("✅ Full APNs JSON payload:\n%s", string(payloadJSON))
}

// --- Test helpers ---

func assertEqual(t *testing.T, field string, expected, actual interface{}) {
	t.Helper()
	if expected != actual {
		t.Errorf("%s: expected %v, got %v", field, expected, actual)
	}
}

func logPayload(t *testing.T, label string, message *PushMessage) {
	t.Helper()
	payload := message.ToAPNsPayload()
	payloadJSON, _ := json.MarshalIndent(payload, "", "  ")
	t.Logf("\n📱 %s APNs Payload:\n%s", label, string(payloadJSON))
}

// TestFollowGate_BlocksNonFollower verifies that notifications are blocked
// when the recipient does not follow the event author.
func TestFollowGate_BlocksNonFollower(t *testing.T) {
	// Recipient's contact list (kind 3) — follows only "friend_pubkey"
	contactList := &nostr.Event{
		ID:        "contact_list_123",
		PubKey:    "recipient_pubkey",
		Kind:      3,
		Tags:      nostr.Tags{{"p", "friend_pubkey"}},
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
	}

	ps := newTestPushService([]*nostr.Event{contactList})
	ps.followGated = true

	// Event from someone the recipient follows — should be allowed
	friendEvent := &nostr.Event{Kind: 1, PubKey: "friend_pubkey"}
	if !ps.recipientFollowsAuthor("recipient_pubkey", "friend_pubkey", friendEvent) {
		t.Error("Expected notification to be allowed for followed author")
	}

	// Event from someone the recipient does NOT follow — should be blocked
	strangerEvent := &nostr.Event{Kind: 1, PubKey: "stranger_pubkey"}
	if ps.recipientFollowsAuthor("recipient_pubkey", "stranger_pubkey", strangerEvent) {
		t.Error("Expected notification to be blocked for non-followed author")
	}
}

// TestFollowGate_AllowsKind1059 verifies that encrypted DMs (Gift Wrap)
// bypass the follow gate since the pubkey is ephemeral.
func TestFollowGate_AllowsKind1059(t *testing.T) {
	ps := newTestPushService(nil)
	ps.followGated = true

	giftWrap := &nostr.Event{Kind: 1059, PubKey: "ephemeral_key"}
	if !ps.recipientFollowsAuthor("recipient_pubkey", "ephemeral_key", giftWrap) {
		t.Error("Expected kind 1059 to bypass follow gate")
	}
}

// TestFollowGate_AllowsTestNotification verifies that test notifications
// (all-zeros pubkey) bypass the follow gate.
func TestFollowGate_AllowsTestNotification(t *testing.T) {
	ps := newTestPushService(nil)
	ps.followGated = true

	testEvent := &nostr.Event{Kind: 1808, PubKey: "0000000000000000000000000000000000000000000000000000000000000000"}
	if !ps.recipientFollowsAuthor("recipient_pubkey", testEvent.PubKey, testEvent) {
		t.Error("Expected test notification to bypass follow gate")
	}
}

// TestFollowGate_AllowsNoContactList verifies that users with no contact list
// still receive all notifications (permissive for new users).
func TestFollowGate_AllowsNoContactList(t *testing.T) {
	// No events in store — recipient has no kind 3
	ps := newTestPushService(nil)
	ps.followGated = true

	event := &nostr.Event{Kind: 1, PubKey: "some_author"}
	if !ps.recipientFollowsAuthor("recipient_pubkey", "some_author", event) {
		t.Error("Expected notification to be allowed when recipient has no contact list")
	}
}