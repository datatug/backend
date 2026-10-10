package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dal-go/record"
)

const AccessProtected = "protected"

// SharedProjectRef preserves the distinction between a Space-owned project
// and the legacy private cloud root, even when their project IDs are equal.
type SharedProjectRef struct {
	StoreID   string `json:"storeId"`
	SpaceID   string `json:"spaceID"`
	ProjectID string `json:"projectId"`
}

// ValidateSharedProjectIdentifier accepts one bounded path segment. Core Space
// authority separately validates that a Space exists and is eligible.
func ValidateSharedProjectIdentifier(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return fmt.Errorf("identifier must contain 1..128 characters")
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("identifier must be a safe path segment")
		}
	}
	return nil
}

func ValidateSharedProjectTitle(title string) error {
	if title == "" || title != strings.TrimSpace(title) || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 200 {
		return fmt.Errorf("title must contain 1..200 characters without outer whitespace")
	}
	for _, c := range title {
		if unicode.IsControl(c) {
			return fmt.Errorf("title must not contain control characters")
		}
	}
	return nil
}

func sharedProjectExtensionKey(spaceID string) *record.Key {
	return record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", spaceID), "ext", "datatug")
}

// NewSharedProjectRecord reuses the existing Project DTO without a private
// userIDs ownership list. The canonical Space path is its ownership binding.
func NewSharedProjectRecord(spaceID, projectID string) (record.Record, *Project) {
	project := new(Project)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), "projects", projectID)
	return record.NewRecordWithData(key, project), project
}

// SharedProjectCreateDigest freezes every command payload field; JSON provides
// unambiguous boundaries even if a caller tries separator-shaped values.
func SharedProjectCreateDigest(actorID, spaceID, commandID, title string) string {
	b, _ := json.Marshal([5]string{"shared-project-create/1", actorID, spaceID, commandID, title})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func PaidSharedProjectCreateDigest(actorID, spaceID, commandID, title, payer, mode, product string) string {
	b, _ := json.Marshal([8]string{"paid-shared-project-create/1", actorID, spaceID, commandID, title, payer, mode, product})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

// SharedProjectCreateReceipt is immutable, private command evidence. It is not
// a project index, a module-enablement record, or a billing entitlement.
type SharedProjectCreateReceipt struct {
	OwnerContact  ProjectOwnerContactProof `json:"ownerContact,omitempty" firestore:"ownerContact,omitempty"`
	Version       int                      `json:"v" firestore:"v"`
	ActorID       string                   `json:"actorID" firestore:"actorID"`
	SpaceID       string                   `json:"spaceID" firestore:"spaceID"`
	CommandID     string                   `json:"commandID" firestore:"commandID"`
	Title         string                   `json:"title" firestore:"title"`
	RequestDigest string                   `json:"requestDigest" firestore:"requestDigest"`
	ProjectID     string                   `json:"projectID" firestore:"projectID"`
	CreatedAt     time.Time                `json:"createdAt" firestore:"createdAt"`
	PayerID       string                   `json:"payerID,omitempty" firestore:"payerID,omitempty"`
	Mode          string                   `json:"mode,omitempty" firestore:"mode,omitempty"`
	Product       string                   `json:"product,omitempty" firestore:"product,omitempty"`
	GitHub        *GitHubCreateSource      `json:"github,omitempty" firestore:"github,omitempty"`
}

// GitHubCreateSource fixes the selected repository, branch head and pinned
// template in the admission receipt. Provider credentials are never included.
type GitHubCreateSource struct {
	Binding        GitHubProjectBinding `json:"binding" firestore:"binding"`
	ExpectedHead   string               `json:"expectedHead" firestore:"expectedHead"`
	TemplateID     string               `json:"templateID" firestore:"templateID"`
	TemplateCommit string               `json:"templateCommit" firestore:"templateCommit"`
}

func GitHubSharedProjectCreateDigest(actorID, spaceID, commandID, title, payer, mode, product string, source GitHubCreateSource) string {
	b, _ := json.Marshal(struct {
		Kind      string             `json:"kind"`
		ActorID   string             `json:"actorID"`
		SpaceID   string             `json:"spaceID"`
		CommandID string             `json:"commandID"`
		Title     string             `json:"title"`
		Payer     string             `json:"payer"`
		Mode      string             `json:"mode"`
		Product   string             `json:"product"`
		Source    GitHubCreateSource `json:"source"`
	}{"github-shared-project-create/1", actorID, spaceID, commandID, title, payer, mode, product, source})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func (r SharedProjectCreateReceipt) Validate() error {
	if r.OwnerContact.Present() {
		if err := r.OwnerContact.Validate(); err != nil {
			return err
		}
	}
	if (r.Version != 1 && r.Version != 2) || r.ActorID == "" || len(r.ActorID) > 128 || r.ActorID != strings.TrimSpace(r.ActorID) || !utf8.ValidString(r.ActorID) || r.CreatedAt.IsZero() || r.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("invalid shared project create receipt")
	}
	for _, id := range []string{r.SpaceID, r.CommandID, r.ProjectID} {
		if err := ValidateSharedProjectIdentifier(id); err != nil {
			return err
		}
	}
	if err := ValidateSharedProjectTitle(r.Title); err != nil {
		return err
	}
	digest := SharedProjectCreateDigest(r.ActorID, r.SpaceID, r.CommandID, r.Title)
	if r.PayerID != "" || r.Mode != "" || r.Product != "" {
		if ValidateSharedProjectIdentifier(r.PayerID) != nil || ValidateSharedProjectIdentifier(r.Product) != nil || (r.Mode != "test" && r.Mode != "live") {
			return fmt.Errorf("invalid paid project binding")
		}
		digest = PaidSharedProjectCreateDigest(r.ActorID, r.SpaceID, r.CommandID, r.Title, r.PayerID, r.Mode, r.Product)
	}
	if r.Version == 2 {
		if r.GitHub == nil || r.GitHub.Binding.Validate() != nil || r.GitHub.ExpectedHead == "" || r.GitHub.TemplateID == "" || r.GitHub.TemplateCommit == "" || r.Mode != "live" || (r.Product != "datatug" && r.Product != "datatug-business-usage") || (r.Product == "datatug-business-usage" && r.PayerID != r.SpaceID) {
			return fmt.Errorf("invalid GitHub shared project source")
		}
		digest = GitHubSharedProjectCreateDigest(r.ActorID, r.SpaceID, r.CommandID, r.Title, r.PayerID, r.Mode, r.Product, *r.GitHub)
	} else if r.GitHub != nil {
		return fmt.Errorf("unexpected GitHub shared project source")
	}
	if r.RequestDigest != digest {
		return fmt.Errorf("invalid shared project request digest")
	}
	return nil
}

func NewSharedProjectCreateReceiptRecord(spaceID, commandID string) (record.Record, *SharedProjectCreateReceipt) {
	digest := sha256.Sum256([]byte(commandID))
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), "projectCreates", hex.EncodeToString(digest[:]))
	receipt := new(SharedProjectCreateReceipt)
	return record.NewRecordWithData(key, receipt), receipt
}
