package access

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	merkle_dag "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
	"github.com/HORNET-Storage/hdk-nostr-go/lib/signing"
	"github.com/HORNET-Storage/hornet-storage/lib/config"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/organization"
	"github.com/HORNET-Storage/hornet-storage/lib/stores"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/statistics"
	"github.com/HORNET-Storage/hornet-storage/lib/types"
	"github.com/HORNET-Storage/hornet-storage/lib/wot"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/nbd-wtf/go-nostr"
)

const (
	// accessCacheTTL is how long access check results are cached.
	accessCacheTTL                = 30 * time.Second
	repositoryPermissionEventKind = 31415
	repositoryPushEventKind       = 73
	repositoryPullRequestKind     = 74
	repositoryPRApprovalKind      = 75
	repositoryCommentKind         = 1111
	repositoryAppDataKind         = 30078
	repositoryVisibilityPrivate   = "private"
	repositoryVisibilityPublic    = "public"

	// Interaction permission levels stored as tag values on the repository permission event.
	interactionPermissionEveryone          = "everyone"
	interactionPermissionWot               = "wot"
	interactionPermissionMaintainersTriage = "maintainers_triage"
	interactionPermissionMaintainers       = "maintainers"
)

// cachedResult stores an access check result with expiry.
type cachedResult struct {
	err       error
	expiresAt time.Time
}

// AccessControl handles permission checking for H.O.R.N.E.T Allowed Users
type AccessControl struct {
	statsStore statistics.StatisticsStore
	settings   *types.AllowedUsersSettings

	// accessCache caches IsAllowed results keyed by "permission:hex"
	accessCache sync.Map

	// WotCache holds parsed WOT social graphs keyed by DAG root hash.
	// Used for "wot" interaction permission checks.
	WotCache *wot.Cache

	// Blacklist holds per-repository blocked pubkeys.
	// Used to deny writes from blacklisted users before any permission checks.
	Blacklist *RepoBlacklist
}

// NewAccessControl creates a new access control instance
func NewAccessControl(statsStore statistics.StatisticsStore, settings *types.AllowedUsersSettings) *AccessControl {
	return &AccessControl{
		statsStore: statsStore,
		settings:   settings,
		WotCache:   wot.NewCache(),
		Blacklist:  NewRepoBlacklist(),
	}
}

func (ac *AccessControl) CanRead(npub string) error {
	return ac.IsAllowed(ac.settings.Read, npub, false)
}

func (ac *AccessControl) CanWrite(npub string) error {
	return ac.IsAllowed(ac.settings.Write, npub, true)
}

func (ac *AccessControl) CanWriteEvent(event *nostr.Event, store stores.Store) error {
	if event == nil {
		return fmt.Errorf("event is required")
	}

	isOrganizationEvent := event.Kind == organization.EventKind ||
		event.Kind == organization.InvitationKind ||
		event.Kind == organization.InvitationResponseKind
	isOrganizationDeletion := event.Kind == organization.DeletionKind &&
		strings.HasPrefix(firstTagValue(event.Tags, "a"), "3950")

	writeErr := ac.CanWrite(event.PubKey)
	if writeErr == nil {
		if isOrganizationEvent || isOrganizationDeletion {
			if store == nil {
				return fmt.Errorf("organization event validation requires a store")
			}
			if err := organization.ValidateAuthorizedWrite(event, store); err != nil {
				return fmt.Errorf("invalid organization authorization event: %w", err)
			}
		}
		return nil
	}

	if !ac.repoAccessOverrideEnabled() || store == nil {
		return writeErr
	}

	if isOrganizationEvent || isOrganizationDeletion {
		if err := organization.ValidateWriteOverride(event, store); err != nil {
			logging.Debugf("[ACCESS CONTROL] Organization access override denied for pubkey %s on kind %d: %v", event.PubKey, event.Kind, err)
			return fmt.Errorf("%w: invalid organization authorization event: %v", writeErr, err)
		}
		logging.Debugf("[ACCESS CONTROL] Organization access override granted for pubkey %s on kind %d", event.PubKey, event.Kind)
		return nil
	}

	if err := ac.canWriteRepositoryEvent(event, store); err != nil {
		logging.Debugf("[ACCESS CONTROL] Repository access override denied for pubkey %s on kind %d: %v", event.PubKey, event.Kind, err)
		return writeErr
	}

	logging.Debugf("[ACCESS CONTROL] Repository access override granted for pubkey %s on kind %d", event.PubKey, event.Kind)
	return nil
}

func (ac *AccessControl) CanReadEvent(event *nostr.Event, requesterPubkey string, store stores.Store) error {
	if event == nil {
		return fmt.Errorf("event is required")
	}

	globalReadErr := ac.CanRead(requesterPubkey)
	if globalReadErr == nil {
		return nil
	}

	if store == nil || !ac.repoReadOverrideEnabled() {
		return globalReadErr
	}

	// Organization definitions, invitations, and acceptance responses do not
	// carry repository r tags. They are nevertheless required proof for clients
	// to authorize organization-owned repository events.
	if canReadOrganizationAuthorizationEvent(event, requesterPubkey, store) {
		return nil
	}

	if !ac.isRepositoryEventEligible(event) {
		return globalReadErr
	}

	permissionEvent, err := ac.getRepositoryPermissionEvent(event, store)
	if err != nil {
		return globalReadErr
	}

	if repositoryPermissionVisibility(permissionEvent) != repositoryVisibilityPrivate {
		return nil
	}

	requesterPubkey = strings.ToLower(strings.TrimSpace(requesterPubkey))
	isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(permissionEvent, requesterPubkey, store)
	if isOrganizationRepo {
		if membershipErr == nil && isMember {
			return nil
		}
		return globalReadErr
	}
	if requesterPubkey != "" && repoPermissionAllowsRead(permissionEvent, requesterPubkey) {
		return nil
	}

	return globalReadErr
}

// CanReadDag checks whether a requester is allowed to download a DAG.
// Takes the root leaf so resolveDagRepoContext can inspect AdditionalData
// and perform the kind 31415 wot_file reverse lookup that fixes the WOT download bug.
func (ac *AccessControl) CanReadDag(rootLeaf *merkle_dag.DagLeaf, requesterPubkey string, requesterSignature string, store stores.Store) error {
	if rootLeaf == nil {
		return fmt.Errorf("root leaf is required")
	}
	root := rootLeaf.Hash

	// Step 1: Normalize pubkey and verify identity if credentials provided.
	// On failure, treat as anonymous rather than trusting an unverified pubkey.
	requesterPubkey = strings.ToLower(strings.TrimSpace(requesterPubkey))
	if requesterPubkey != "" && requesterSignature != "" {
		if err := verifyRootRequestSignature(root, requesterPubkey, requesterSignature); err != nil {
			logging.Debugf("[ACCESS CONTROL] DAG download identity verification failed for %s: %v — treating as anonymous", requesterPubkey, err)
			requesterPubkey = ""
		}
	}

	// Step 2: Global read access passes → allow
	globalReadErr := ac.CanRead(requesterPubkey)
	if globalReadErr == nil {
		return nil
	}

	// Step 3: Override disabled → deny
	if store == nil || !ac.repoReadOverrideEnabled() {
		return globalReadErr
	}

	// Step 4: Resolve DAG to repository context (includes kind 31415 wot_file reverse lookup)
	permissionEvent, _, err := ac.resolveDagRepoContext(root, rootLeaf, store)
	if err != nil {
		return globalReadErr
	}

	// Step 5: Blacklist check
	repoID := firstTagValue(permissionEvent.Tags, "r")
	if requesterPubkey != "" && ac.Blacklist != nil && ac.Blacklist.IsBlacklisted(repoID, requesterPubkey) {
		return fmt.Errorf("pubkey is blacklisted from this repository")
	}

	// Step 6: Non-private repos → allow (THIS IS THE BUG FIX)
	// WOT files, public repo bundles/archives, etc. are downloadable by anyone.
	if repositoryPermissionVisibility(permissionEvent) != repositoryVisibilityPrivate {
		return nil
	}

	// Step 7: Private repo → require a verified identity. Organization membership
	// is authoritative for organization repositories; personal repos keep their
	// explicit permission roles.
	if requesterPubkey == "" {
		return globalReadErr
	}
	isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(permissionEvent, requesterPubkey, store)
	if isOrganizationRepo {
		if membershipErr == nil && isMember {
			return nil
		}
		return globalReadErr
	}
	if repoPermissionAllowsRead(permissionEvent, requesterPubkey) {
		return nil
	}

	return globalReadErr
}

// dagClass identifies the type of DAG for write-side permission classification.
type dagClass string

const (
	dagClassWotFile  dagClass = "wot_file"
	dagClassBundle   dagClass = "bundle"
	dagClassArchive  dagClass = "archive"
	dagClassPRBundle dagClass = "pr_bundle"
)

// CanWriteDag checks whether a pubkey is allowed to upload a DAG.
// Takes the root leaf for AdditionalData-based classification (r tag, wot_file,
// pr_bundle) and the verified pubkey (signature already verified by upload handler).
//
// When AdditionalData contains an "r" tag (PR bundles, WOT files), precise
// per-repo permission checking is used. When "r" is absent (push bundles,
// archives, forked DAGs), falls back to the broad collaborator check.
// The DAG-embedded r is a pointer/optimization, never an authority — events
// pointing at DAGs are the authority, and precise per-repo enforcement happens
// at the event handler layer when the associating event arrives.
func (ac *AccessControl) CanWriteDag(rootLeaf *merkle_dag.DagLeaf, pubkey string, store stores.Store) error {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))

	// Step 1: Global write access → allow (relay owner / allowed users upload anything)
	globalWriteErr := ac.CanWrite(pubkey)
	if globalWriteErr == nil {
		return nil
	}

	// Step 2: Override disabled → deny
	if store == nil || !ac.repoAccessOverrideEnabled() {
		return globalWriteErr
	}

	if pubkey == "" || !isValidHexPubkey(pubkey) {
		return globalWriteErr
	}

	// Step 3: Try precise per-repo check if "r" tag is present in AdditionalData.
	// PR bundles and WOT files carry r (never re-used/forked). Push bundles and
	// archives do NOT carry r (they get re-used in forks).
	rTag := ""
	if rootLeaf != nil && rootLeaf.AdditionalData != nil {
		rTag = rootLeaf.AdditionalData["r"]
	}

	if rTag != "" {
		permissionEvent, err := ac.findRepositoryPermissionEvent(rTag, store)
		if err != nil {
			return fmt.Errorf("repository permission event not found for repo %s: %w", rTag, err)
		}

		// Blacklist check (precise — we know the repo)
		if ac.Blacklist != nil && ac.Blacklist.IsBlacklisted(rTag, pubkey) {
			logging.Debugf("[ACCESS CONTROL] Blacklisted pubkey %s denied DAG upload to repo %s", pubkey, rTag)
			return fmt.Errorf("pubkey is blacklisted from this repository")
		}

		return ac.canWriteDagWithRepoContext(rootLeaf, pubkey, rTag, permissionEvent, store)
	}

	// Step 4: No "r" tag — fall back to broad collaborator check.
	// This covers push bundles/archives (collaborators only), cross-relay forks
	// (DAGs transferred as-is), and any flow that can't embed r.
	logging.Debugf("[ACCESS CONTROL] DAG upload without r tag from %s — using broad collaborator fallback", pubkey)

	// WOT files without r: verify uploader == wot_owner, then broad check
	if rootLeaf != nil && rootLeaf.AdditionalData != nil && rootLeaf.AdditionalData["wot_file"] == "true" {
		wotOwner := strings.ToLower(strings.TrimSpace(rootLeaf.AdditionalData["wot_owner"]))
		if pubkey != wotOwner {
			return fmt.Errorf("WOT file upload denied: uploader (%s) does not match wot_owner (%s)", pubkey, wotOwner)
		}
	}

	// Broad check: is this user an owner or collaborator on any repo on this relay?
	if ac.isRepoOwnerOrCollaborator(pubkey, store) {
		return nil
	}

	return fmt.Errorf("pubkey does not have DAG write access through any repository")
}

// canWriteDagWithRepoContext performs class-based write checks when the precise
// repo context is known ("r" tag present and permission event resolved).
func (ac *AccessControl) canWriteDagWithRepoContext(rootLeaf *merkle_dag.DagLeaf, pubkey string, rTag string, permissionEvent *nostr.Event, store stores.Store) error {
	if rootLeaf != nil && rootLeaf.AdditionalData != nil && rootLeaf.AdditionalData["wot_file"] == "true" {
		// WOT file: three-key rule — uploader == wot_owner == permission event author.
		// Owner only. This also prevents junk from entering the WOT cache.
		wotOwner := strings.ToLower(strings.TrimSpace(rootLeaf.AdditionalData["wot_owner"]))
		peAuthor := strings.ToLower(strings.TrimSpace(permissionEvent.PubKey))
		if pubkey != wotOwner || pubkey != peAuthor {
			return fmt.Errorf("WOT file upload denied: uploader (%s), wot_owner (%s), and repo owner (%s) must all match", pubkey, wotOwner, peAuthor)
		}
		return nil
	}

	isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(permissionEvent, pubkey, store)
	if membershipErr != nil {
		return fmt.Errorf("failed to verify repository organization membership: %w", membershipErr)
	}
	if isOrganizationRepo {
		if isMember {
			return nil
		}
		return fmt.Errorf("pubkey %s is not an active member of the repository organization", pubkey)
	}

	if rootLeaf != nil && rootLeaf.AdditionalData != nil && rootLeaf.AdditionalData["pr_bundle"] == "true" {
		// PR bundle: use pr_permissions dropdown chain
		prPerm := strings.ToLower(strings.TrimSpace(firstTagValue(permissionEvent.Tags, "pr_permissions")))
		switch prPerm {
		case interactionPermissionEveryone:
			if repositoryPermissionVisibility(permissionEvent) != repositoryVisibilityPrivate {
				return nil
			}
		case interactionPermissionWot:
			if ac.repoWotPermissionAllowsWrite(permissionEvent, pubkey) {
				return nil
			}
		case interactionPermissionMaintainersTriage:
			role := getUserRoleFromPermissionEvent(permissionEvent, pubkey)
			if role == "owner" || role == "maintainer" || role == "triage" {
				return nil
			}
		case interactionPermissionMaintainers:
			role := getUserRoleFromPermissionEvent(permissionEvent, pubkey)
			if role == "owner" || role == "maintainer" {
				return nil
			}
		}
		return fmt.Errorf("pubkey %s does not have PR bundle upload permission for repo %s", pubkey, rTag)
	}

	// Default: any DAG with r — owner/maintainer/write role
	if repoPermissionAllowsDagWrite(permissionEvent, pubkey) {
		return nil
	}

	return fmt.Errorf("pubkey %s does not have DAG write access for repo %s", pubkey, rTag)
}

// isRepoOwnerOrCollaborator checks if a pubkey is an owner or collaborator on
// any repo, or if any public repo allows WoT/everyone-based PR creation.
// Used as a fallback when no "r" tag is present in DAG AdditionalData.
func (ac *AccessControl) isRepoOwnerOrCollaborator(pubkey string, store stores.Store) bool {
	permissionEvents, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPermissionEventKind},
	})
	if err != nil || len(permissionEvents) == 0 {
		return false
	}

	for _, event := range permissionEvents {
		isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(event, pubkey, store)
		if isOrganizationRepo {
			if membershipErr == nil && isMember {
				return true
			}
			continue
		}
		// Personal repository owner can always upload.
		if strings.ToLower(event.PubKey) == pubkey {
			return true
		}
		// Any p tag entry means the user is a collaborator.
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "p" && strings.ToLower(tag[1]) == pubkey {
				return true
			}
		}
	}

	// Not a collaborator anywhere — check if any public repo allows PR
	// creation (which requires DAG upload for the bundle)
	for _, event := range permissionEvents {
		if repositoryPermissionVisibility(event) == repositoryVisibilityPrivate {
			continue
		}
		prPerm := strings.ToLower(strings.TrimSpace(firstTagValue(event.Tags, "pr_permissions")))
		if prPerm == interactionPermissionEveryone || prPerm == interactionPermissionWot {
			return true
		}
	}

	return false
}

// resolveDagRepoContext resolves a DAG root to its repository permission event
// and classification. Replaces findRepositoryPermissionEventByRoot with broader
// lookups including kind 31415 wot_file tags — the root cause fix for the WOT
// download bug.
//
// Lookup order:
//  1. AdditionalData "r" pointer (fast path) → findRepositoryPermissionEvent(r).
//     For wot_file class: binding = event's wot_file tag == root; on failure fall through.
//  2. Reverse lookups (authoritative): kind 31415 wot_file=root, kind 73 bundle/archive,
//     kind 74 dag_root. Derive repoID from the found event's r tag.
//  3. Nothing found → error (unassociated DAG).
func (ac *AccessControl) resolveDagRepoContext(root string, rootLeaf *merkle_dag.DagLeaf, store stores.Store) (*nostr.Event, dagClass, error) {
	// Determine class from AdditionalData
	var class dagClass
	if rootLeaf != nil && rootLeaf.AdditionalData != nil {
		if rootLeaf.AdditionalData["wot_file"] == "true" {
			class = dagClassWotFile
		} else if rootLeaf.AdditionalData["pr_bundle"] == "true" {
			class = dagClassPRBundle
		}
	}

	// Fast path: AdditionalData "r" pointer
	if rootLeaf != nil && rootLeaf.AdditionalData != nil {
		if rTag := rootLeaf.AdditionalData["r"]; rTag != "" {
			pe, err := ac.findRepositoryPermissionEvent(rTag, store)
			if err == nil {
				// For wot_file class: verify binding — the permission event's
				// wot_file tag must reference this exact DAG root.
				if class == dagClassWotFile {
					if firstTagValue(pe.Tags, "wot_file") == root {
						return pe, class, nil
					}
					// Binding failed — fall through to reverse lookups
					logging.Debugf("[ACCESS CONTROL] WOT DAG %s has r=%s but permission event wot_file tag does not match — falling through to reverse lookup", root, rTag)
				} else {
					return pe, class, nil
				}
			}
		}
	}

	// Reverse lookup 1: kind 31415 with wot_file=root (THE BUG FIX)
	if events, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPermissionEventKind},
		Tags:  nostr.TagMap{"wot_file": []string{root}},
		Limit: 1,
	}); err == nil && len(events) > 0 {
		repoID := firstTagValue(events[0].Tags, "r")
		if repoID != "" {
			pe, err := ac.findRepositoryPermissionEvent(repoID, store)
			if err == nil {
				return pe, dagClassWotFile, nil
			}
		}
	}

	// Reverse lookup 2: kind 73 bundle/archive tags
	for _, tagName := range []string{"bundle", "archive"} {
		if events, err := store.QueryEvents(nostr.Filter{
			Kinds: []int{repositoryPushEventKind},
			Tags:  nostr.TagMap{tagName: []string{root}},
			Limit: 1,
		}); err == nil && len(events) > 0 {
			repoID := firstTagValue(events[0].Tags, "r")
			if repoID != "" {
				pe, err := ac.findRepositoryPermissionEvent(repoID, store)
				if err == nil {
					resolvedClass := dagClassBundle
					if tagName == "archive" {
						resolvedClass = dagClassArchive
					}
					return pe, resolvedClass, nil
				}
			}
		}
	}

	// Reverse lookup 3: kind 74 dag_root tag
	if events, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPullRequestKind},
		Tags:  nostr.TagMap{"dag_root": []string{root}},
		Limit: 1,
	}); err == nil && len(events) > 0 {
		repoID := firstTagValue(events[0].Tags, "r")
		if repoID != "" {
			pe, err := ac.findRepositoryPermissionEvent(repoID, store)
			if err == nil {
				return pe, dagClassPRBundle, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no repository context found for DAG root %s", root)
}

func (ac *AccessControl) IsAllowed(readOrWrite string, npub string, requireWriteCapability bool) error {
	readOrWrite = normalizeAccessSetting(readOrWrite)

	// Everyone is allowed if all_users is set
	if readOrWrite == "all_users" {
		return nil
	}

	// Validate that the public key is a valid 64-char hex string.
	// Per NIP-01, clients must send hex-encoded 32-byte pubkeys.
	if !isValidHexPubkey(npub) {
		return fmt.Errorf("invalid public key format: expected 64-character hex string")
	}
	hex := strings.ToLower(npub)

	// Check cache first
	cacheMode := "read"
	if requireWriteCapability {
		cacheMode = "write"
	}
	cacheKey := readOrWrite + ":" + cacheMode + ":" + hex
	if cached, ok := ac.accessCache.Load(cacheKey); ok {
		entry := cached.(*cachedResult)
		if time.Now().Before(entry.expiresAt) {
			return entry.err
		}
		// Expired — delete and re-check
		ac.accessCache.Delete(cacheKey)
	}

	// Perform the actual access check
	result := ac.isAllowedUncached(readOrWrite, hex, requireWriteCapability)

	// Cache the result
	ac.accessCache.Store(cacheKey, &cachedResult{
		err:       result,
		expiresAt: time.Now().Add(accessCacheTTL),
	})

	return result
}

func (ac *AccessControl) repoAccessOverrideEnabled() bool {
	if ac.settings == nil {
		return false
	}

	return normalizeAccessSetting(ac.settings.Mode) == "invite-only" &&
		normalizeAccessSetting(ac.settings.Write) == "allowed_users" &&
		len(ac.settings.RepoAccessOverrideKinds) > 0
}

func (ac *AccessControl) repoReadOverrideEnabled() bool {
	if ac.settings == nil {
		return false
	}

	return normalizeAccessSetting(ac.settings.Mode) == "invite-only" &&
		normalizeAccessSetting(ac.settings.Read) == "allowed_users" &&
		len(ac.settings.RepoAccessOverrideKinds) > 0
}

func (ac *AccessControl) repositoryAccessKindsConfigured() bool {
	return ac.settings != nil && len(ac.settings.RepoAccessOverrideKinds) > 0
}

func (ac *AccessControl) isRepositoryEventEligible(event *nostr.Event) bool {
	if event == nil || !ac.repositoryAccessKindsConfigured() {
		return false
	}

	if firstTagValue(event.Tags, "r") == "" {
		return false
	}

	return containsKind(ac.settings.RepoAccessOverrideKinds, event.Kind)
}

func (ac *AccessControl) getRepositoryPermissionEvent(event *nostr.Event, store stores.Store) (*nostr.Event, error) {
	if event == nil {
		return nil, fmt.Errorf("event is required")
	}

	repoID := firstTagValue(event.Tags, "r")
	if repoID == "" {
		return nil, fmt.Errorf("missing repository identifier tag")
	}

	if event.Kind == repositoryPermissionEventKind {
		return event, nil
	}

	return ac.findRepositoryPermissionEvent(repoID, store)
}

func (ac *AccessControl) findRepositoryPermissionEvent(repoID string, store stores.Store) (*nostr.Event, error) {
	permissionEvents, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPermissionEventKind},
		Tags:  nostr.TagMap{"r": []string{repoID}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query repository permission event: %w", err)
	}
	if len(permissionEvents) == 0 {
		return nil, fmt.Errorf("repository permission event not found")
	}

	return latestRepositoryPermissionEvent(permissionEvents), nil
}

func latestRepositoryPermissionEvent(events []*nostr.Event) *nostr.Event {
	if len(events) == 0 {
		return nil
	}

	sort.SliceStable(events, func(i, j int) bool {
		left := events[i]
		right := events[j]

		leftCreated := int64(0)
		rightCreated := int64(0)
		if left != nil {
			leftCreated = left.CreatedAt.Time().Unix()
		}
		if right != nil {
			rightCreated = right.CreatedAt.Time().Unix()
		}

		if leftCreated != rightCreated {
			return leftCreated > rightCreated
		}

		leftID := ""
		rightID := ""
		if left != nil {
			leftID = left.ID
		}
		if right != nil {
			rightID = right.ID
		}

		return leftID > rightID
	})

	return events[0]
}

func (ac *AccessControl) canWriteRepositoryEvent(event *nostr.Event, store stores.Store) error {
	if !containsKind(ac.settings.RepoAccessOverrideKinds, event.Kind) {
		return fmt.Errorf("event kind %d is not eligible for repository access override", event.Kind)
	}

	pubkey := strings.ToLower(event.PubKey)
	if !isValidHexPubkey(pubkey) {
		return fmt.Errorf("invalid public key format: expected 64-character hex string")
	}

	repoID := firstTagValue(event.Tags, "r")
	if repoID == "" {
		return fmt.Errorf("missing repository identifier tag")
	}

	// Check blacklist before any permission checks — blacklisted users are
	// denied immediately regardless of their role or interaction permissions.
	if ac.Blacklist != nil && ac.Blacklist.IsBlacklisted(repoID, pubkey) {
		logging.Debugf("[ACCESS CONTROL] Blacklisted pubkey %s denied write to repo %s", pubkey, repoID)
		return fmt.Errorf("pubkey is blacklisted from this repository")
	}

	permissionEvents, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPermissionEventKind},
		Tags:  nostr.TagMap{"r": []string{repoID}},
	})
	if err != nil {
		return fmt.Errorf("failed to query repository permission event: %w", err)
	}
	permissionEvent := latestRepositoryPermissionEvent(permissionEvents)
	if permissionEvent == nil {
		// Repository-scoped authority cannot exist before the first permission event.
		// Because this override is reached only after ordinary relay write admission failed,
		// organization membership must not be allowed to bootstrap a repository here.
		return fmt.Errorf("repository permission event not found")
	}

	// Permission policy is administrative state, not an ordinary repository write.
	// Match the kind 31415 handler: personal repositories are controlled by their
	// current event author, while organization repositories are controlled by the
	// organization owner. Ordinary organization membership remains sufficient only
	// for non-administrative repository operations below.
	if event.Kind == repositoryPermissionEventKind {
		return canUpdateRepositoryPermissionEvent(permissionEvent, pubkey, store)
	}

	isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(permissionEvent, pubkey, store)
	if membershipErr != nil {
		return fmt.Errorf("failed to check organization membership: %w", membershipErr)
	}
	if isOrganizationRepo {
		if isMember {
			return nil
		}
		return fmt.Errorf("pubkey is not an active member of the repository organization")
	}

	// Check standard role-based access first
	if repositoryEventRequiresDagWrite(event) {
		if repoPermissionAllowsDagWrite(permissionEvent, pubkey) {
			return nil
		}
	} else if repoPermissionAllowsEventWrite(permissionEvent, pubkey) {
		return nil
	}

	// Standard role check failed — try interaction permission fallback
	interactionTag := interactionPermissionTagForEvent(event)
	if interactionTag != "" {
		permLevel := strings.ToLower(strings.TrimSpace(firstTagValue(permissionEvent.Tags, interactionTag)))

		// WoT check: use the WOT cache for real follow-distance verification
		if permLevel == interactionPermissionWot {
			if ac.repoWotPermissionAllowsWrite(permissionEvent, pubkey) {
				logging.Debugf("[ACCESS CONTROL] WoT permission override granted for pubkey %s on kind %d via %s", pubkey, event.Kind, interactionTag)
				return nil
			}
		} else if repoInteractionPermissionAllowsWrite(permissionEvent, pubkey, interactionTag) {
			logging.Debugf("[ACCESS CONTROL] Repository interaction permission override granted for pubkey %s on kind %d via %s", pubkey, event.Kind, interactionTag)
			return nil
		}
	}

	// All checks failed
	if repositoryEventRequiresDagWrite(event) {
		return fmt.Errorf("pubkey does not have repository DAG write access")
	}
	return fmt.Errorf("pubkey does not have repository event write access")
}

func canUpdateRepositoryPermissionEvent(permissionEvent *nostr.Event, pubkey string, store stores.Store) error {
	if permissionEvent == nil {
		return fmt.Errorf("repository permission event not found")
	}

	organizationAddressValue := strings.TrimSpace(firstTagValue(permissionEvent.Tags, "a"))
	if organizationAddressValue == "" {
		if strings.ToLower(strings.TrimSpace(permissionEvent.PubKey)) == pubkey {
			return nil
		}
		return fmt.Errorf("only the repository owner may update repository permissions")
	}

	organizationAddress, err := organization.ParseAddress(organizationAddressValue)
	if err != nil {
		return fmt.Errorf("invalid repository organization address: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(organizationAddress.Owner)) != pubkey {
		return fmt.Errorf("only the organization owner may update repository permissions")
	}

	isMember, err := organization.IsMember(store, pubkey, organizationAddress)
	if err != nil {
		return fmt.Errorf("failed to verify repository organization owner: %w", err)
	}
	if !isMember {
		return fmt.Errorf("organization owner is not an active member of the repository organization")
	}

	return nil
}

func organizationMembershipAllows(permissionEvent *nostr.Event, pubkey string, store stores.Store) (bool, bool, error) {
	if permissionEvent == nil || store == nil {
		return false, false, nil
	}
	addressValue := firstTagValue(permissionEvent.Tags, "a")
	if addressValue == "" {
		return false, false, nil
	}
	address, err := organization.ParseAddress(addressValue)
	if err != nil {
		return true, false, fmt.Errorf("invalid repository organization address: %w", err)
	}
	isMember, err := organization.IsMember(store, strings.ToLower(strings.TrimSpace(pubkey)), address)
	return true, isMember, err
}

func canReadOrganizationAuthorizationEvent(event *nostr.Event, requesterPubkey string, store stores.Store) bool {
	address, ok := organizationAuthorizationEventAddress(event)
	if !ok {
		return false
	}

	requesterPubkey = strings.ToLower(strings.TrimSpace(requesterPubkey))
	if requesterPubkey != "" {
		isMember, err := organization.IsMember(store, requesterPubkey, address)
		if err == nil && isMember {
			return true
		}
	}

	return organizationHasPublicRepository(store, address)
}

func organizationAuthorizationEventAddress(event *nostr.Event) (organization.Address, bool) {
	if event == nil {
		return organization.Address{}, false
	}

	switch event.Kind {
	case organization.EventKind:
		address := organization.Address{
			Owner: strings.ToLower(strings.TrimSpace(event.PubKey)),
			DTag:  strings.TrimSpace(firstTagValue(event.Tags, "d")),
		}
		parsed, err := organization.ParseAddress(address.String())
		return parsed, err == nil
	case organization.InvitationKind, organization.InvitationResponseKind:
		address, err := organization.ParseAddress(firstTagValue(event.Tags, "a"))
		return address, err == nil
	default:
		return organization.Address{}, false
	}
}

func organizationHasPublicRepository(store stores.Store, address organization.Address) bool {
	permissionEvents, err := store.QueryEvents(nostr.Filter{
		Kinds: []int{repositoryPermissionEventKind},
		Tags:  nostr.TagMap{"a": []string{address.String()}},
	})
	if err != nil {
		return false
	}

	eventsByRepository := make(map[string][]*nostr.Event)
	for _, permissionEvent := range permissionEvents {
		if permissionEvent == nil {
			continue
		}
		repoID := strings.TrimSpace(firstTagValue(permissionEvent.Tags, "r"))
		if repoID == "" {
			continue
		}
		eventsByRepository[repoID] = append(eventsByRepository[repoID], permissionEvent)
	}

	for _, repositoryEvents := range eventsByRepository {
		permissionEvent := latestRepositoryPermissionEvent(repositoryEvents)
		if permissionEvent == nil || repositoryPermissionVisibility(permissionEvent) == repositoryVisibilityPrivate {
			continue
		}

		isOrganizationRepo, isMember, membershipErr := organizationMembershipAllows(permissionEvent, permissionEvent.PubKey, store)
		if isOrganizationRepo && membershipErr == nil && isMember {
			return true
		}
	}

	return false
}

func firstTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func containsKind(kinds []int, kind int) bool {
	for _, allowedKind := range kinds {
		if allowedKind == kind {
			return true
		}
	}
	return false
}

func repoPermissionAllowsRead(permissionEvent *nostr.Event, pubkey string) bool {
	if permissionEvent == nil {
		return false
	}

	if strings.ToLower(permissionEvent.PubKey) == pubkey {
		return true
	}

	for _, tag := range permissionEvent.Tags {
		if len(tag) < 2 || tag[0] != "p" || strings.ToLower(tag[1]) != pubkey {
			continue
		}
		return true
	}

	return false
}

func repositoryPermissionVisibility(permissionEvent *nostr.Event) string {
	if permissionEvent == nil {
		return repositoryVisibilityPublic
	}

	visibility := strings.ToLower(strings.TrimSpace(firstTagValue(permissionEvent.Tags, "visibility")))
	if visibility == repositoryVisibilityPrivate {
		return repositoryVisibilityPrivate
	}

	return repositoryVisibilityPublic
}

func repoPermissionAllowsEventWrite(permissionEvent *nostr.Event, pubkey string) bool {
	if permissionEvent == nil {
		return false
	}

	if strings.ToLower(permissionEvent.PubKey) == pubkey {
		return true
	}

	for _, tag := range permissionEvent.Tags {
		if len(tag) < 3 || tag[0] != "p" || strings.ToLower(tag[1]) != pubkey {
			continue
		}

		for _, roleValue := range tag[2:] {
			switch strings.ToLower(strings.TrimSpace(roleValue)) {
			case "maintainer", "write", "triage", "encrypted-write":
				return true
			}
		}
	}

	return false
}

func repoPermissionAllowsDagWrite(permissionEvent *nostr.Event, pubkey string) bool {
	if permissionEvent == nil {
		return false
	}

	if strings.ToLower(permissionEvent.PubKey) == pubkey {
		return true
	}

	for _, tag := range permissionEvent.Tags {
		if len(tag) < 3 || tag[0] != "p" || strings.ToLower(tag[1]) != pubkey {
			continue
		}

		for _, roleValue := range tag[2:] {
			switch strings.ToLower(strings.TrimSpace(roleValue)) {
			case "maintainer", "write":
				return true
			}
		}
	}

	return false
}

func repositoryEventRequiresDagWrite(event *nostr.Event) bool {
	if event == nil {
		return false
	}

	switch event.Kind {
	case repositoryPushEventKind, repositoryPullRequestKind:
		return true
	}

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}

		switch tag[0] {
		case "bundle", "archive", "dag_root":
			return true
		}
	}

	return false
}

func verifyRootRequestSignature(root string, requesterPubkey string, requesterSignature string) error {
	publicKey, err := signing.DeserializePublicKey(requesterPubkey)
	if err != nil {
		return err
	}

	signatureBytes, err := hex.DecodeString(requesterSignature)
	if err != nil {
		return err
	}

	parsedSignature, err := schnorr.ParseSignature(signatureBytes)
	if err != nil {
		return err
	}

	return signing.VerifySerializedCIDSignature(parsedSignature, root, publicKey)
}

// interactionPermissionTagForEvent maps a Nostr event to its interaction
// permission tag name on the repository permission event. For kinds that serve
// multiple purposes (1111 comments, 30078 app data), the event's own tags are
// inspected to determine which interaction permission applies.
// Returns "" if the event has no interaction permission tag.
func interactionPermissionTagForEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}

	switch event.Kind {
	case repositoryPullRequestKind: // 74
		return "pr_permissions"

	case repositoryPRApprovalKind: // 75 — PR reviews/approvals
		return "pr_comment_permissions"

	case repositoryCommentKind: // 1111 — issue or PR comments
		// Determine comment type from the I tag path.
		iTagValue := firstTagValue(event.Tags, "I")
		if iTagValue == "" {
			iTagValue = firstTagValue(event.Tags, "i")
		}
		if strings.Contains(iTagValue, "/issues/") {
			return "issue_comment_permissions"
		}
		if strings.Contains(iTagValue, "/pull-requests/") {
			return "pr_comment_permissions"
		}
		return "" // Unknown comment target — no interaction override

	case repositoryAppDataKind: // 30078 — irisdb app-specific data
		// Determine data type from the d tag path.
		dTagValue := firstTagValue(event.Tags, "d")
		if strings.Contains(dTagValue, "/blacklist/") {
			return "" // Blacklist entries are owner/maintainer only — handled separately
		}
		if strings.Contains(dTagValue, "/issues/") {
			return "issue_permissions"
		}
		if strings.Contains(dTagValue, "/pull-requests/") || strings.Contains(dTagValue, "/prs/") {
			return "pr_permissions"
		}
		if strings.Contains(dTagValue, "/kanban/") || strings.Contains(dTagValue, "/projects/") {
			return "kanban_permissions"
		}
		return "" // Unknown app data path — no interaction override

	default:
		return ""
	}
}

// getUserRoleFromPermissionEvent returns the role a pubkey holds on a repository
// permission event: "owner", "maintainer", "triage", "write", "read", or "".
func getUserRoleFromPermissionEvent(permissionEvent *nostr.Event, pubkey string) string {
	if permissionEvent == nil {
		return ""
	}
	if strings.ToLower(permissionEvent.PubKey) == pubkey {
		return "owner"
	}
	for _, tag := range permissionEvent.Tags {
		if len(tag) < 3 || tag[0] != "p" || strings.ToLower(tag[1]) != pubkey {
			continue
		}
		for _, roleValue := range tag[2:] {
			role := strings.ToLower(strings.TrimSpace(roleValue))
			switch role {
			case "maintainer", "triage", "write", "read":
				return role
			}
		}
	}
	return ""
}

// repoInteractionPermissionAllowsWrite checks whether the interaction permission
// tag on a permission event grants write access to the given pubkey.
// For "wot" the repo must also be public (private repos ignore WoT).
func repoInteractionPermissionAllowsWrite(permissionEvent *nostr.Event, pubkey string, interactionTag string) bool {
	if permissionEvent == nil || interactionTag == "" {
		return false
	}

	permissionLevel := strings.ToLower(strings.TrimSpace(firstTagValue(permissionEvent.Tags, interactionTag)))
	if permissionLevel == "" {
		return false
	}

	switch permissionLevel {
	case interactionPermissionEveryone:
		// "everyone" allows any signed-in user, but only for public repos
		return repositoryPermissionVisibility(permissionEvent) != repositoryVisibilityPrivate
	case interactionPermissionWot:
		// WoT requires the requester to be within N follow-hops of the repo owner's social graph.
		// Only for public repos — private repos ignore WoT.
		if repositoryPermissionVisibility(permissionEvent) == repositoryVisibilityPrivate {
			return false
		}
		// The real WoT check is done via repoWotPermissionAllowsWrite which has access to the cache.
		// This switch case returns true as a fallback — the actual WoT gating happens in
		// canWriteRepositoryEvent which calls repoWotPermissionAllowsWrite before falling back here.
		// If we reach this point, it means the caller didn't have a WoT cache available,
		// so we deny by default for safety.
		return false
	case interactionPermissionMaintainersTriage:
		role := getUserRoleFromPermissionEvent(permissionEvent, pubkey)
		return role == "owner" || role == "maintainer" || role == "triage"
	case interactionPermissionMaintainers:
		role := getUserRoleFromPermissionEvent(permissionEvent, pubkey)
		return role == "owner" || role == "maintainer"
	default:
		return false
	}
}

// repoWotPermissionAllowsWrite checks whether a pubkey is within the configured
// follow-distance of the repository owner's WOT graph. Reads the wot_file and
// wot_hops tags from the permission event and delegates to the WOT cache.
// Only for public repos — private repos always return false.
func (ac *AccessControl) repoWotPermissionAllowsWrite(permissionEvent *nostr.Event, pubkey string) bool {
	if permissionEvent == nil || ac.WotCache == nil {
		return false
	}

	// WoT is only for public repos
	if repositoryPermissionVisibility(permissionEvent) == repositoryVisibilityPrivate {
		return false
	}

	// Read the DAG root hash of the uploaded WOT file
	wotFileHash := strings.TrimSpace(firstTagValue(permissionEvent.Tags, "wot_file"))
	if wotFileHash == "" {
		logging.Debugf("[ACCESS CONTROL] WoT check failed: no wot_file tag on permission event")
		return false
	}

	// Read the configured max hops (default 3, clamped to 1-5)
	maxHops := wot.DefaultMaxHops
	if hopsStr := strings.TrimSpace(firstTagValue(permissionEvent.Tags, "wot_hops")); hopsStr != "" {
		if parsed, err := strconv.Atoi(hopsStr); err == nil {
			if parsed >= 1 && parsed <= wot.MaxAllowedHops {
				maxHops = parsed
			}
		}
	}

	if ac.WotCache.IsWithinHops(wotFileHash, pubkey, maxHops) {
		logging.Debugf("[ACCESS CONTROL] WoT check passed: pubkey %s is within %d hops (root hash: %s)", pubkey, maxHops, wotFileHash)
		return true
	}

	logging.Debugf("[ACCESS CONTROL] WoT check failed: pubkey %s is NOT within %d hops (root hash: %s)", pubkey, maxHops, wotFileHash)
	return false
}

// isAllowedUncached performs the actual DB-backed access check (no cache).
func (ac *AccessControl) isAllowedUncached(readOrWrite string, hex string, requireWriteCapability bool) error {
	logging.Debugf("Access check - Permission: %s, Mode: %s", readOrWrite, ac.settings.Mode)

	// The owner is always allowed
	if ac.isOwner(hex) {
		logging.Debugf("[ACCESS CONTROL] User %s is the relay owner, granting access", hex)
		return nil
	}

	if readOrWrite == "only-me" {
		return fmt.Errorf("user does not have permission")
	}

	// Get the allowed user from the database
	user, err := ac.statsStore.GetAllowedUser(hex)
	if err != nil {
		logging.Debugf("[ACCESS CONTROL] Error looking up user %s: %v", hex, err)
		return err
	}

	// User is not allowed if they don't exist
	if user == nil {
		logging.Debugf("[ACCESS CONTROL] User %s not found in allowed_users table", hex)
		return fmt.Errorf("user does not have permission to read")
	}

	if requireWriteCapability && user.ReadOnly {
		logging.Debugf("[ACCESS CONTROL] User %s is allowed for read but not write", hex)
		return fmt.Errorf("user does not have permission to write")
	}

	logging.Debugf("[ACCESS CONTROL] User %s found with tier: %s", hex, user.Tier)

	// Check if user has a paid tier if set to paid_users
	if readOrWrite == "paid_users" {
		logging.Debugf("[ACCESS CONTROL] Checking paid subscriber status for user: %s", hex)
		paidSubscriber, err := ac.statsStore.GetPaidSubscriberByNpub(hex)
		if err != nil {
			logging.Debugf("[ACCESS CONTROL] Error checking paid subscriber status: %v", err)
			return fmt.Errorf("user does not have permission")
		}

		if paidSubscriber == nil {
			logging.Debugf("[ACCESS CONTROL] User %s not found in paid subscribers table", hex)
			return fmt.Errorf("user does not have a paid subscription")
		}

		// Check if subscription is still valid
		if time.Now().After(paidSubscriber.ExpirationDate) {
			logging.Debugf("[ACCESS CONTROL] User %s subscription expired on %v", hex, paidSubscriber.ExpirationDate)
			return fmt.Errorf("user subscription has expired")
		}

		if paidSubscriber.Tier == "" {
			logging.Debugf("[ACCESS CONTROL] User %s has empty tier", hex)
			return fmt.Errorf("user does not have a valid subscription tier")
		}

		// Check if the tier is actually a paid tier
		cfg, err := config.GetConfig()
		if err == nil && cfg != nil {
			tierIsPaid := false
			for _, tier := range cfg.AllowedUsersSettings.Tiers {
				if tier.Name == paidSubscriber.Tier && tier.PriceSats > 0 {
					tierIsPaid = true
					break
				}
			}
			if !tierIsPaid {
				logging.Debugf("[ACCESS CONTROL] User %s has free/unpaid tier: %s", hex, paidSubscriber.Tier)
				return fmt.Errorf("user has a free tier, not a paid subscription")
			}
		} else {
			if strings.Contains(strings.ToLower(paidSubscriber.Tier), "free") || strings.Contains(strings.ToLower(paidSubscriber.Tier), "basic") {
				logging.Debugf("[ACCESS CONTROL] User %s has free tier: %s", hex, paidSubscriber.Tier)
				return fmt.Errorf("user has a free tier, not a paid subscription")
			}
		}

		logging.Debugf("[ACCESS CONTROL] User %s has valid paid subscription: %s", hex, paidSubscriber.Tier)
	}

	return nil
}

func (ac *AccessControl) AddAllowedUser(npub string, read bool, write bool, tier string, createdBy string) error {
	canWrite := write
	if !read && !write {
		canWrite = false
	}
	return ac.statsStore.AddAllowedUser(npub, canWrite, tier, createdBy)
}

func (ac *AccessControl) RemoveAllowedUser(npub string) error {
	return ac.statsStore.RemoveAllowedUser(npub)
}

// isValidHexPubkey checks if s is a valid 64-character hex-encoded public key.
func isValidHexPubkey(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Is the incoming pub key the owner of the relay
func (ac *AccessControl) isOwner(hex string) bool {
	// First check database for relay owner
	if ac.statsStore != nil {
		owner, err := ac.statsStore.GetRelayOwner()
		if err == nil && owner != nil {
			ownerHex := strings.ToLower(owner.Npub)
			if hex == ownerHex {
				return true
			}
		}
	}

	// Fallback to config-based owner (for backwards compatibility)
	_, err := config.GetConfig()
	if err != nil {
		return false
	}

	// Note: The relay public key is not in the Config struct,
	// so we need to get it from the relay settings
	// For now, we'll skip this check if we can't get the config
	// The database check above should be sufficient
	return false
}

// ValidateSettings validates the access control settings for consistency
func (ac *AccessControl) ValidateSettings(settings *types.AllowedUsersSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// Validate mode
	mode := normalizeAccessSetting(settings.Mode)
	read := normalizeAccessSetting(settings.Read)
	write := normalizeAccessSetting(settings.Write)

	logging.Debugf("Write setting %s", write)
	// This ensures the correct options are selected for each mode and sets defaults when incorrect values are set
	// Not all read/write values are valid for each mode so this ensures that the read/write values are in line with the selected mode
	// mode: 		only-me, invite_only, public, subscription
	// read/write: 	all_users, paid_users, allowed_users, only-me

	switch mode {
	case "only-me":
		write = "only-me"
		switch read {
		case "only-me":
		case "all_users":
		case "allowed_users":
		default:
			read = "only-me"
		}
	case "invite-only":
		write = "allowed_users"
		switch read {
		case "all_users":
		case "allowed_users":
		default:
			read = "allowed_users"
		}
	case "public":
		write = "all_users"
		read = "all_users"
	case "subscription":
		write = "paid_users"
		switch read {
		case "all_users":
		case "paid_users":
		default:
			read = "paid_users"
		}
	default:
		mode = "only-me"
		read = "only-me"
		write = "only-me"
	}

	settings.Mode = mode
	settings.Read = read
	settings.Write = write

	return nil
}

func normalizeAccessSetting(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "only_me":
		return "only-me"
	case "invite_only":
		return "invite-only"
	default:
		return normalized
	}
}

// GetSettings returns the current access control settings
func (ac *AccessControl) GetSettings() *types.AllowedUsersSettings {
	return ac.settings
}

// UpdateSettings updates the access control settings and invalidates the access cache
func (ac *AccessControl) UpdateSettings(settings *types.AllowedUsersSettings) {
	logging.Infof("Updating access control settings - Mode: %s, Read: %s, Write: %s",
		settings.Mode, settings.Read, settings.Write)
	ac.settings = settings
	// Invalidate all cached access results since settings changed
	ac.InvalidateCache()
}

// InvalidateCache clears all cached access check results.
// Call this when allowed users, subscriptions, or settings change.
func (ac *AccessControl) InvalidateCache() {
	ac.accessCache.Range(func(key, value interface{}) bool {
		ac.accessCache.Delete(key)
		return true
	})
}
