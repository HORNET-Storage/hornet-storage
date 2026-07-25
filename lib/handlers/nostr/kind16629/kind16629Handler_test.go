package kind16629

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/nbd-wtf/go-nostr"
)

func cloneURLForValidationTest(repoID, repoAuthor, repoName string) string {
	query := url.Values{}
	query.Set("id", repoID)
	query.Set("repo_author", repoAuthor)
	query.Set("repo_name", repoName)
	return "nosis://relay.example?" + query.Encode()
}

func TestValidateCloneTagAcceptsOrganizationAddressRepresentations(t *testing.T) {
	ownerPubkey := strings.Repeat("a", 64)
	otherOwnerPubkey := strings.Repeat("b", 64)
	repoID := "3343072f-9540-6250-9400-e4e33008d27a"
	repoName := "org-repo"
	aTag := "39504:" + ownerPubkey + ":nosis-organization-test"

	tests := []struct {
		name       string
		repoAuthor string
		wantValid  bool
	}{
		{
			name:       "canonical organization address",
			repoAuthor: aTag,
			wantValid:  true,
		},
		{
			name:       "filesystem-safe organization address",
			repoAuthor: "39504_" + ownerPubkey + "_nosis-organization-test",
			wantValid:  true,
		},
		{
			name:       "different organization identifier",
			repoAuthor: "39504_" + ownerPubkey + "_nosis-organization-other",
			wantValid:  false,
		},
		{
			name:       "different organization owner",
			repoAuthor: "39504_" + otherOwnerPubkey + "_nosis-organization-test",
			wantValid:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cloneURL := cloneURLForValidationTest(repoID, test.repoAuthor, repoName)
			validationError := validateCloneTag(cloneURL, repoID, repoName, aTag, ownerPubkey)

			if test.wantValid && validationError != "" {
				t.Fatalf("expected clone tag to be accepted, got %q", validationError)
			}
			if !test.wantValid && validationError == "" {
				t.Fatal("expected clone tag to be rejected")
			}
		})
	}
}

func TestValidateCloneTagKeepsPersonalAuthorValidation(t *testing.T) {
	publisherPubkey := strings.Repeat("a", 64)
	otherPubkey := strings.Repeat("b", 64)
	repoID := "3343072f-9540-6250-9400-e4e33008d27a"
	repoName := "personal-repo"

	matchingCloneURL := cloneURLForValidationTest(repoID, publisherPubkey, repoName)
	if validationError := validateCloneTag(matchingCloneURL, repoID, repoName, "", publisherPubkey); validationError != "" {
		t.Fatalf("expected matching personal repo author to be accepted, got %q", validationError)
	}

	mismatchedCloneURL := cloneURLForValidationTest(repoID, otherPubkey, repoName)
	if validationError := validateCloneTag(mismatchedCloneURL, repoID, repoName, "", publisherPubkey); validationError == "" {
		t.Fatal("expected mismatched personal repo author to be rejected")
	}
}

func TestVerifyPublisherPermissionRequiresValidOrganizationProof(t *testing.T) {
	store, err := badgerhold.InitStore(filepath.Join(t.TempDir(), "store"), filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	defer store.Cleanup()

	owner := strings.Repeat("a", 64)
	member := strings.Repeat("b", 64)
	orgDTag := "nosis-organization-handler-test"
	orgAddress := fmt.Sprintf("39504:%s:%s", owner, orgDTag)

	if verifyPublisherPermission(store, owner, true, owner, orgDTag, true, false) {
		t.Fatal("expected organization owner to be denied when the organization event is absent")
	}

	organizationEvent := &nostr.Event{
		ID:        fmt.Sprintf("%064x", 1),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(100),
		Kind:      39504,
		Tags: nostr.Tags{
			{"d", orgDTag},
			{"p", owner, "member"},
			{"p", member, "member"},
		},
	}
	if err := store.StoreEvent(organizationEvent); err != nil {
		t.Fatalf("StoreEvent(organization): %v", err)
	}
	if !verifyPublisherPermission(store, owner, true, owner, orgDTag, true, false) {
		t.Fatal("expected organization owner to be allowed after a valid organization exists")
	}
	if verifyPublisherPermission(store, member, true, owner, orgDTag, true, false) {
		t.Fatal("expected listed member without invitation acceptance proof to be denied")
	}

	invitation := &nostr.Event{
		ID:        fmt.Sprintf("%064x", 2),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(101),
		Kind:      39505,
		Tags: nostr.Tags{
			{"d", "nosis-org-invite-handler-test"},
			{"p", member},
			{"a", orgAddress},
		},
	}
	response := &nostr.Event{
		ID:        fmt.Sprintf("%064x", 3),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(102),
		Kind:      39506,
		Tags: nostr.Tags{
			{"d", "nosis-org-response-nosis-org-invite-handler-test"},
			{"e", invitation.ID},
			{"status", "accepted"},
			{"a", orgAddress},
		},
	}
	for _, event := range []*nostr.Event{invitation, response} {
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("StoreEvent(kind %d): %v", event.Kind, err)
		}
	}
	if !verifyPublisherPermission(store, member, true, owner, orgDTag, true, false) {
		t.Fatal("expected member with matching owner invitation and accepted response to be allowed")
	}
	if verifyPublisherPermission(store, member, true, owner, orgDTag, false, false) {
		t.Fatal("expected non-owner member to remain unable to replace repository permissions")
	}

	removedOrganizationEvent := &nostr.Event{
		ID:        fmt.Sprintf("%064x", 4),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(200),
		Kind:      39504,
		Tags: nostr.Tags{
			{"d", orgDTag},
			{"p", owner, "member"},
			{"p", member, "removed"},
		},
	}
	if err := store.StoreEvent(removedOrganizationEvent); err != nil {
		t.Fatalf("StoreEvent(removed organization): %v", err)
	}
	if verifyPublisherPermission(store, member, true, owner, orgDTag, true, false) {
		t.Fatal("expected member marked removed in the latest organization event to be denied")
	}
}
