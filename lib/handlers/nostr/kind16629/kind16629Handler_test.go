package kind16629

import (
	"net/url"
	"strings"
	"testing"
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
