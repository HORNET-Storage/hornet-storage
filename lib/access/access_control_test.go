package access_test

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/access"
	"github.com/HORNET-Storage/hornet-storage/lib/organization"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/badgerhold"
	"github.com/HORNET-Storage/hornet-storage/lib/types"
	"github.com/nbd-wtf/go-nostr"
)

func TestCanWriteEventAllowsRepoCollaboratorInInviteOnlyMode(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	owner := newAccessTestPubkey(t)
	maintainer := newAccessTestPubkey(t)
	triage := newAccessTestPubkey(t)
	stranger := newAccessTestPubkey(t)
	repoID := "11111111-1111-1111-1111-111111111111"

	permissionEvent := &nostr.Event{
		ID:        accessTestEventID(1),
		PubKey:    owner,
		CreatedAt: nostr.Now(),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"p", maintainer, "maintainer"},
			{"p", triage, "triage"},
		},
	}
	if err := store.StoreEvent(permissionEvent); err != nil {
		t.Fatalf("StoreEvent: %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "all_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 16630},
	})

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: maintainer, Kind: 73, Tags: nostr.Tags{{"r", repoID}}}, store); err != nil {
		t.Fatalf("expected maintainer to be allowed for repo event: %v", err)
	}

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: triage, Kind: 16630, Tags: nostr.Tags{{"r", repoID}}}, store); err != nil {
		t.Fatalf("expected triage collaborator to be allowed for repo metadata event: %v", err)
	}

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: triage, Kind: 73, Tags: nostr.Tags{{"r", repoID}}}, store); err == nil {
		t.Fatal("expected triage collaborator to be denied for DAG-writing repo event")
	}

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: maintainer, Kind: 1, Tags: nostr.Tags{{"r", repoID}}}, store); err == nil {
		t.Fatal("expected unconfigured kind to be denied")
	}

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: maintainer, Kind: 73}, store); err == nil {
		t.Fatal("expected repo event without r tag to be denied")
	}

	if err := accessControl.CanWriteEvent(&nostr.Event{PubKey: stranger, Kind: 73, Tags: nostr.Tags{{"r", repoID}}}, store); err == nil {
		t.Fatal("expected pubkey without repo permission to be denied")
	}
}

func TestCanWriteRequiresWriteCapableAllowedUser(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	readOnlyUser := newAccessTestPubkey(t)
	writer := newAccessTestPubkey(t)

	if err := store.GetStatsStore().AddAllowedUser(readOnlyUser, false, "", "test"); err != nil {
		t.Fatalf("AddAllowedUser(read-only): %v", err)
	}
	if err := store.GetStatsStore().AddAllowedUser(writer, true, "", "test"); err != nil {
		t.Fatalf("AddAllowedUser(writer): %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 74, 31415, 30078},
	})

	if err := accessControl.CanRead(readOnlyUser); err != nil {
		t.Fatalf("expected read-only allowed user to keep read access: %v", err)
	}
	if err := accessControl.CanWrite(readOnlyUser); err == nil {
		t.Fatal("expected read-only allowed user to be denied write access")
	}
	if err := accessControl.CanWrite(writer); err != nil {
		t.Fatalf("expected write-capable allowed user to keep write access: %v", err)
	}
}

func TestCanReadEventUsesLatestRepositoryPermissionEvent(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	owner := newAccessTestPubkey(t)
	maintainer := newAccessTestPubkey(t)
	repoID := "22222222-2222-2222-2222-222222222222"

	olderPermissionEvent := &nostr.Event{
		ID:        accessTestEventID(10),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(100),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"visibility", "private"},
		},
	}
	if err := store.StoreEvent(olderPermissionEvent); err != nil {
		t.Fatalf("StoreEvent(olderPermissionEvent): %v", err)
	}

	newerPermissionEvent := &nostr.Event{
		ID:        accessTestEventID(11),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(200),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"visibility", "private"},
			{"p", maintainer, "maintainer"},
		},
	}
	if err := store.StoreEvent(newerPermissionEvent); err != nil {
		t.Fatalf("StoreEvent(newerPermissionEvent): %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 74, 31415, 30078},
	})

	repoIssueEvent := &nostr.Event{
		ID:        accessTestEventID(12),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(300),
		Kind:      30078,
		Tags: nostr.Tags{
			{"r", repoID},
			{"d", "/apps/git/repos/22222222-2222-2222-2222-222222222222/issues/33333333-3333-3333-3333-333333333333/title"},
		},
	}

	if err := accessControl.CanReadEvent(repoIssueEvent, maintainer, store); err != nil {
		t.Fatalf("expected maintainer to be allowed to read repo event using latest permission event: %v", err)
	}
}

func TestCanReadDagAllowsMaintainerWhenBundleTagResolvesRepo(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, err := nostr.GetPublicKey(ownerPriv)
	if err != nil {
		t.Fatalf("GetPublicKey(owner): %v", err)
	}

	maintainerPriv := nostr.GeneratePrivateKey()
	maintainerPub, err := nostr.GetPublicKey(maintainerPriv)
	if err != nil {
		t.Fatalf("GetPublicKey(maintainer): %v", err)
	}

	repoID := "33333333-3333-3333-3333-333333333333"
	bundleRoot := "bafireieolfptoisxqb4ghgqimimt6v75igt7pqdnl3plibmz3n2fwbpge4"

	permissionEvent := &nostr.Event{
		ID:        accessTestEventID(20),
		PubKey:    ownerPub,
		CreatedAt: nostr.Timestamp(100),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"visibility", "private"},
			{"p", maintainerPub, "maintainer"},
		},
	}
	if err := store.StoreEvent(permissionEvent); err != nil {
		t.Fatalf("StoreEvent(permissionEvent): %v", err)
	}

	pushEvent := &nostr.Event{
		ID:        accessTestEventID(21),
		PubKey:    ownerPub,
		CreatedAt: nostr.Timestamp(101),
		Kind:      73,
		Tags: nostr.Tags{
			{"r", repoID},
			{"bundle", bundleRoot},
		},
	}
	if err := store.StoreEvent(pushEvent); err != nil {
		t.Fatalf("StoreEvent(pushEvent): %v", err)
	}

	privateKey, _, err := signing.DeserializePrivateKey(maintainerPriv)
	if err != nil {
		t.Fatalf("DeserializePrivateKey: %v", err)
	}
	serializedPubkey, err := signing.SerializePublicKey(privateKey.PubKey())
	if err != nil {
		t.Fatalf("SerializePublicKey: %v", err)
	}
	if *serializedPubkey != maintainerPub {
		t.Fatalf("expected serialized pubkey %s to match nostr pubkey %s", *serializedPubkey, maintainerPub)
	}

	signature, err := signing.SignSerializedCid(bundleRoot, privateKey)
	if err != nil {
		t.Fatalf("SignSerializedCid: %v", err)
	}
	if err := signing.VerifySerializedCIDSignature(signature, bundleRoot, privateKey.PubKey()); err != nil {
		t.Fatalf("VerifySerializedCIDSignature: %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 74, 31415, 30078},
	})

	bundleEvents, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{73},
		Tags: nostr.TagMap{
			"bundle": []string{bundleRoot},
		},
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("QueryEvents(bundle): %v", err)
	}
	if len(bundleEvents) != 1 {
		t.Fatalf("expected 1 bundle event, got %d", len(bundleEvents))
	}

	if err := accessControl.CanReadEvent(&nostr.Event{Kind: 73, Tags: nostr.Tags{{"r", repoID}}}, maintainerPub, store); err != nil {
		t.Fatalf("expected maintainer to be allowed to read repo event directly: %v", err)
	}

	if err := accessControl.CanReadDag(&merkle_dag.DagLeaf{Hash: bundleRoot}, maintainerPub, hex.EncodeToString(signature.Serialize()), store); err != nil {
		t.Fatalf("expected maintainer to be allowed to read bundle DAG: %v", err)
	}
}

func TestRepositoryReadOverrideDisabledInOnlyMeMode(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	owner := newAccessTestPubkey(t)
	repoID := "44444444-4444-4444-4444-444444444444"
	bundleRoot := "bafireihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	readerPriv := nostr.GeneratePrivateKey()
	readerPub, err := nostr.GetPublicKey(readerPriv)
	if err != nil {
		t.Fatalf("GetPublicKey(reader): %v", err)
	}

	permissionEvent := &nostr.Event{
		ID:        accessTestEventID(30),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(100),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"visibility", "public"},
			{"p", readerPub, "read"},
		},
	}
	if err := store.StoreEvent(permissionEvent); err != nil {
		t.Fatalf("StoreEvent(permissionEvent): %v", err)
	}

	pushEvent := &nostr.Event{
		ID:        accessTestEventID(31),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(101),
		Kind:      73,
		Tags: nostr.Tags{
			{"r", repoID},
			{"bundle", bundleRoot},
		},
	}
	if err := store.StoreEvent(pushEvent); err != nil {
		t.Fatalf("StoreEvent(pushEvent): %v", err)
	}

	privateKey, _, err := signing.DeserializePrivateKey(readerPriv)
	if err != nil {
		t.Fatalf("DeserializePrivateKey(reader): %v", err)
	}
	signature, err := signing.SignSerializedCid(bundleRoot, privateKey)
	if err != nil {
		t.Fatalf("SignSerializedCid: %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "only-me",
		Read:                    "only-me",
		Write:                   "only-me",
		RepoAccessOverrideKinds: []int{73, 74, 31415, 30078},
	})

	if err := accessControl.CanReadEvent(&nostr.Event{Kind: 73, Tags: nostr.Tags{{"r", repoID}}}, readerPub, store); err == nil {
		t.Fatal("expected repo read override to be disabled in only-me mode")
	}

	if err := accessControl.CanReadDag(&merkle_dag.DagLeaf{Hash: bundleRoot}, readerPub, hex.EncodeToString(signature.Serialize()), store); err == nil {
		t.Fatal("expected DAG read override to be disabled in only-me mode")
	}
}

func TestCanWriteEventValidatesOrganizationProofChain(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	owner := newAccessTestPubkey(t)
	member := newAccessTestPubkey(t)
	stranger := newAccessTestPubkey(t)
	orgDTag := "nosis-organization-access-test"
	orgAddress := fmt.Sprintf("39504:%s:%s", owner, orgDTag)

	organizationEvent := &nostr.Event{
		ID:        accessTestEventID(40),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(100),
		Kind:      39504,
		Tags: nostr.Tags{
			{"d", orgDTag},
			{"p", owner, "member"},
			{"p", member, "member"},
		},
	}
	invitation := &nostr.Event{
		ID:        accessTestEventID(41),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(101),
		Kind:      39505,
		Tags: nostr.Tags{
			{"d", "nosis-org-invite-access-test"},
			{"p", member},
			{"a", orgAddress},
		},
	}
	responseDTag := "nosis-org-response-nosis-org-invite-access-test"
	response := &nostr.Event{
		ID:        accessTestEventID(42),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(102),
		Kind:      39506,
		Tags: nostr.Tags{
			{"d", responseDTag},
			{"e", invitation.ID},
			{"status", "accepted"},
			{"a", orgAddress},
		},
	}
	for _, event := range []*nostr.Event{organizationEvent, invitation, response} {
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("StoreEvent(kind %d): %v", event.Kind, err)
		}
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 31415},
	})

	if err := accessControl.CanWriteEvent(invitation, store); err != nil {
		t.Fatalf("expected valid owner invitation to be allowed: %v", err)
	}
	if err := accessControl.CanWriteEvent(response, store); err != nil {
		t.Fatalf("expected valid invitee response to be allowed: %v", err)
	}
	firstOrganizationRepository := &nostr.Event{
		PubKey: member,
		Kind:   31415,
		Tags: nostr.Tags{
			{"r", "55555555-5555-5555-5555-555555555555"},
			{"a", orgAddress},
		},
	}
	if err := accessControl.CanWriteEvent(firstOrganizationRepository, store); err == nil {
		t.Fatal("expected organization membership alone to be insufficient for creating the first repository permission event")
	}

	// The first permission event still succeeds when the publisher has ordinary relay
	// write admission. Organization membership is then validated by the kind 31415 handler.
	if err := store.GetStatsStore().AddAllowedUser(owner, true, "", "test"); err != nil {
		t.Fatalf("AddAllowedUser(owner): %v", err)
	}
	accessControl = access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 31415},
	})
	firstOrganizationRepository.PubKey = owner
	if err := accessControl.CanWriteEvent(firstOrganizationRepository, store); err != nil {
		t.Fatalf("expected an ordinarily admitted organization owner to create the first repository permission event: %v", err)
	}
	firstOrganizationRepository.ID = accessTestEventID(48)
	firstOrganizationRepository.CreatedAt = nostr.Timestamp(107)
	if err := store.StoreEvent(firstOrganizationRepository); err != nil {
		t.Fatalf("StoreEvent(first organization repository): %v", err)
	}

	// Recreate access control after removing the owner's global admission so the checks
	// below exercise repository-scoped overrides rather than the global access cache.
	if err := store.GetStatsStore().RemoveAllowedUser(owner); err != nil {
		t.Fatalf("RemoveAllowedUser(owner): %v", err)
	}
	accessControl = access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 31415},
	})

	ownerPermissionUpdate := &nostr.Event{
		PubKey: owner,
		Kind:   31415,
		Tags: nostr.Tags{
			{"r", "55555555-5555-5555-5555-555555555555"},
			{"a", orgAddress},
		},
	}
	if err := accessControl.CanWriteEvent(ownerPermissionUpdate, store); err != nil {
		t.Fatalf("expected the organization owner to update repository permissions through the repository override: %v", err)
	}

	memberPermissionUpdate := &nostr.Event{
		PubKey: member,
		Kind:   31415,
		Tags: nostr.Tags{
			{"r", "55555555-5555-5555-5555-555555555555"},
			{"a", orgAddress},
		},
	}
	if err := accessControl.CanWriteEvent(memberPermissionUpdate, store); err == nil {
		t.Fatal("expected an ordinary organization member to be denied permission-event updates")
	}

	memberPush := &nostr.Event{
		PubKey: member,
		Kind:   73,
		Tags: nostr.Tags{
			{"r", "55555555-5555-5555-5555-555555555555"},
		},
	}
	if err := accessControl.CanWriteEvent(memberPush, store); err != nil {
		t.Fatalf("expected active organization member to write subsequent repository events: %v", err)
	}
	strangerPush := &nostr.Event{
		PubKey: stranger,
		Kind:   73,
		Tags: nostr.Tags{
			{"r", "55555555-5555-5555-5555-555555555555"},
		},
	}
	if err := accessControl.CanWriteEvent(strangerPush, store); err == nil {
		t.Fatal("expected a non-member without an explicit repository role to be denied")
	}

	// Global relay write access must not bypass organization proof validation.
	if err := store.GetStatsStore().AddAllowedUser(stranger, true, "", "test"); err != nil {
		t.Fatalf("AddAllowedUser(stranger): %v", err)
	}

	forgedInvitation := &nostr.Event{
		PubKey: stranger,
		Kind:   39505,
		Tags: nostr.Tags{
			{"d", "forged-invite"},
			{"p", member},
			{"a", orgAddress},
		},
	}
	if err := accessControl.CanWriteEvent(forgedInvitation, store); err == nil {
		t.Fatal("expected invitation not authored by the organization owner to be denied")
	}

	forgedResponse := &nostr.Event{
		PubKey: stranger,
		Kind:   39506,
		Tags: nostr.Tags{
			{"d", responseDTag},
			{"e", invitation.ID},
			{"status", "accepted"},
			{"a", orgAddress},
		},
	}
	if err := accessControl.CanWriteEvent(forgedResponse, store); err == nil {
		t.Fatal("expected response author that does not match the invitee to be denied")
	}

	validLeave := &nostr.Event{
		ID:        accessTestEventID(44),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(103),
		Kind:      5,
		Tags: nostr.Tags{
			{"e", response.ID},
			{"a", fmt.Sprintf("39506:%s:%s", member, responseDTag)},
			{"k", "39506"},
		},
	}
	if err := accessControl.CanWriteEvent(validLeave, store); err != nil {
		t.Fatalf("expected invitee to be allowed to delete their own current response: %v", err)
	}
	if err := store.StoreEvent(validLeave); err != nil {
		t.Fatalf("StoreEvent(valid leave tombstone): %v", err)
	}
	if active, err := organization.IsMember(store, member, organization.Address{Owner: owner, DTag: orgDTag}); err != nil {
		t.Fatalf("IsMember after leave: %v", err)
	} else if active {
		t.Fatal("expected stored response tombstone to revoke organization membership")
	}
	if err := accessControl.CanWriteEvent(memberPush, store); err == nil {
		t.Fatal("expected a departed organization member to be denied despite stale permission-event roles")
	}
	if err := accessControl.CanWriteEvent(response, store); err == nil {
		t.Fatal("expected an older tombstoned response to be denied")
	}

	futureInvitation := &nostr.Event{
		ID:        accessTestEventID(45),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(104),
		Kind:      39505,
		Tags: nostr.Tags{
			{"d", "nosis-org-invite-future-test"},
			{"p", member},
			{"a", orgAddress},
		},
	}
	futureResponse := &nostr.Event{
		ID:        accessTestEventID(46),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(105),
		Kind:      39506,
		Tags: nostr.Tags{
			{"d", "nosis-org-response-nosis-org-invite-future-test"},
			{"e", futureInvitation.ID},
			{"status", "accepted"},
			{"a", orgAddress},
		},
	}
	if err := store.StoreEvent(futureInvitation); err != nil {
		t.Fatalf("StoreEvent(future invitation): %v", err)
	}
	preemptiveTombstone := &nostr.Event{
		ID:        accessTestEventID(47),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(106),
		Kind:      5,
		Tags: nostr.Tags{
			{"e", futureResponse.ID},
			{"a", fmt.Sprintf("39506:%s:%s", member, "nosis-org-response-nosis-org-invite-future-test")},
			{"k", "39506"},
		},
	}
	if err := accessControl.CanWriteEvent(preemptiveTombstone, store); err != nil {
		t.Fatalf("expected an author-owned organization tombstone to be retained before its source arrives: %v", err)
	}
	if err := store.StoreEvent(preemptiveTombstone); err != nil {
		t.Fatalf("StoreEvent(preemptive tombstone): %v", err)
	}
	if err := accessControl.CanWriteEvent(futureResponse, store); err == nil {
		t.Fatal("expected preemptive tombstone to prevent older response resurrection")
	}

	removedOrganizationEvent := &nostr.Event{
		ID:        accessTestEventID(43),
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
	if err := accessControl.CanWriteEvent(response, store); err == nil {
		t.Fatal("expected response from a member marked removed by the latest organization event to be denied")
	}
}

func TestCanReadEventAllowsRequiredOrganizationAuthorizationProof(t *testing.T) {
	store := newAccessTestStore(t)
	defer store.Cleanup()

	owner := newAccessTestPubkey(t)
	member := newAccessTestPubkey(t)
	stranger := newAccessTestPubkey(t)
	orgDTag := "nosis-organization-read-proof-test"
	orgAddress := fmt.Sprintf("39504:%s:%s", owner, orgDTag)
	repoID := "66666666-6666-6666-6666-666666666666"

	organizationEvent := &nostr.Event{
		ID:        accessTestEventID(60),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(100),
		Kind:      organization.EventKind,
		Tags: nostr.Tags{
			{"d", orgDTag},
			{"p", owner, "member"},
			{"p", member, "member"},
		},
	}
	invitation := &nostr.Event{
		ID:        accessTestEventID(61),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(101),
		Kind:      organization.InvitationKind,
		Tags: nostr.Tags{
			{"d", "nosis-org-invite-read-proof-test"},
			{"p", member},
			{"a", orgAddress},
		},
	}
	response := &nostr.Event{
		ID:        accessTestEventID(62),
		PubKey:    member,
		CreatedAt: nostr.Timestamp(102),
		Kind:      organization.InvitationResponseKind,
		Tags: nostr.Tags{
			{"d", "nosis-org-response-nosis-org-invite-read-proof-test"},
			{"e", invitation.ID},
			{"status", "accepted"},
			{"a", orgAddress},
		},
	}
	for _, event := range []*nostr.Event{organizationEvent, invitation, response} {
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("StoreEvent(kind %d): %v", event.Kind, err)
		}
	}

	privatePermission := &nostr.Event{
		ID:        accessTestEventID(63),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(103),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"a", orgAddress},
			{"visibility", "private"},
		},
	}
	if err := store.StoreEvent(privatePermission); err != nil {
		t.Fatalf("StoreEvent(private permission): %v", err)
	}

	accessControl := access.NewAccessControl(store.GetStatsStore(), &types.AllowedUsersSettings{
		Mode:                    "invite-only",
		Read:                    "allowed_users",
		Write:                   "allowed_users",
		RepoAccessOverrideKinds: []int{73, 31415},
	})
	proofEvents := []*nostr.Event{organizationEvent, invitation, response}

	for _, event := range proofEvents {
		if err := accessControl.CanReadEvent(event, member, store); err != nil {
			t.Fatalf("expected active member to read private organization proof kind %d: %v", event.Kind, err)
		}
		if err := accessControl.CanReadEvent(event, stranger, store); err == nil {
			t.Fatalf("expected stranger to be denied private organization proof kind %d", event.Kind)
		}
	}

	publicPermission := &nostr.Event{
		ID:        accessTestEventID(64),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(104),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"a", orgAddress},
			{"visibility", "public"},
		},
	}
	if err := store.StoreEvent(publicPermission); err != nil {
		t.Fatalf("StoreEvent(public permission): %v", err)
	}
	for _, event := range proofEvents {
		if err := accessControl.CanReadEvent(event, "", store); err != nil {
			t.Fatalf("expected anonymous reader to obtain public organization proof kind %d: %v", event.Kind, err)
		}
	}

	privateAgain := &nostr.Event{
		ID:        accessTestEventID(65),
		PubKey:    owner,
		CreatedAt: nostr.Timestamp(105),
		Kind:      31415,
		Tags: nostr.Tags{
			{"r", repoID},
			{"a", orgAddress},
			{"visibility", "private"},
		},
	}
	if err := store.StoreEvent(privateAgain); err != nil {
		t.Fatalf("StoreEvent(private-again permission): %v", err)
	}
	if err := accessControl.CanReadEvent(organizationEvent, stranger, store); err == nil {
		t.Fatal("expected the latest private repository state to hide organization proof from strangers")
	}
}

func newAccessTestStore(t *testing.T) *badgerhold.BadgerholdStore {
	t.Helper()

	tempDir := t.TempDir()
	store, err := badgerhold.InitStore(filepath.Join(tempDir, "store"), filepath.Join(tempDir, "stats.db"))
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	return store
}

func newAccessTestPubkey(t *testing.T) string {
	t.Helper()

	publicKey, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	return publicKey
}

func accessTestEventID(sequence int) string {
	return fmt.Sprintf("%064x", sequence)
}
