package facade4datatug

import (
	"context"
	"errors"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

var ErrGitHubProjectNotRegistered = errors.New("GitHub project is not registered")

type GitHubProjectAccess struct {
	SpaceID         string
	SharedProjectID string
	Binding         models4datatug.GitHubProjectBinding
}

// AuthorizeGitHubProjectWrite adds a current LIVE Pro or Business grant to registered
// linked-contact membership. GitHub itself independently decides whether the
// current actor can write; Space roles are not a second GitHub edit ACL.
func (s *SharedProjectService) AuthorizeGitHubProjectWrite(ctx context.Context, actorID string, repositoryID int64, owner, name, folder string) (GitHubProjectAccess, error) {
	access, err := s.ResolveGitHubProject(ctx, actorID, repositoryID, owner, name, folder)
	if err != nil {
		return access, err
	}
	if (s.paid == nil && s.business == nil) || s.now == nil {
		return GitHubProjectAccess{}, ErrSharedProjectUnavailable
	}
	at := s.now().UTC()
	err = s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(access.SpaceID, access.SharedProjectID)
		if err := tx.Get(txCtx, projectRecord); err != nil || project.Status != models4datatug.GitHubProjectReady || project.GitHub == nil || project.GitHub.RepositoryID != repositoryID {
			return ErrSharedProjectUnauthorized
		}
		projectRef := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(access.SpaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: access.SharedProjectID}}
		admission, err := readLinkedProjectAdmission(txCtx, tx, projectRef, project)
		if err != nil || admission == nil {
			return ErrSharedProjectUnauthorized
		}
		return s.verifyCurrentProjectService(txCtx, tx, admission, at)
	})
	if err != nil {
		return GitHubProjectAccess{}, err
	}
	return access, nil
}

// ResolveGitHubProject verifies a ready immutable locator, paid admission
// provenance and a CURRENT active linked contact for this Firebase UID. It
// deliberately does not require an active paid term: reads survive a Pro plan
// ending. The caller also needs fresh GitHub user+App read authority for the
// exact immutable repository; this Space proof never replaces that check.
func (s *SharedProjectService) ResolveGitHubProject(ctx context.Context, actorID string, repositoryID int64, owner, name, folder string) (GitHubProjectAccess, error) {
	var result GitHubProjectAccess
	if s == nil || sharedProjectPortAbsent(s.db) || s.ownerLinks == nil || sharedProjectPortAbsent(s.ownerLinks.contacts) || actorID == "" || repositoryID < 1 {
		return result, ErrSharedProjectUnavailable
	}
	err := s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		locatorRecord, locator := models4datatug.NewGitHubProjectLocatorRecord(repositoryID, folder)
		if err := tx.Get(txCtx, locatorRecord); err != nil {
			if record.IsNotFound(err) {
				return ErrGitHubProjectNotRegistered
			}
			return err
		}
		if locator.Validate() != nil || locator.Status != models4datatug.GitHubProjectReady {
			return ErrSharedProjectUnauthorized
		}
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(locator.SpaceID, locator.ProjectID)
		if err := tx.Get(txCtx, projectRecord); err != nil {
			return ErrSharedProjectUnauthorized
		}
		b := project.GitHub
		if b == nil || b.Validate() != nil || b.RepositoryID != repositoryID || b.Folder != folder || !strings.EqualFold(b.Owner, owner) || !strings.EqualFold(b.Name, name) || project.Storage != models4datatug.GithubStoreID || project.Status != models4datatug.GitHubProjectReady || project.Access != models4datatug.AccessProtected || len(project.UserIDs) != 0 {
			return ErrSharedProjectUnauthorized
		}
		projectRef := contract4linkage.RelationshipEntityRef{
			SpaceID: coretypes.SpaceID(locator.SpaceID),
			ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: locator.ProjectID},
		}
		if _, err := readCurrentProjectMember(txCtx, tx, *s.ownerLinks, projectRef, project, actorID); err != nil {
			return ErrSharedProjectUnauthorized
		}
		result = GitHubProjectAccess{SpaceID: locator.SpaceID, SharedProjectID: locator.ProjectID, Binding: *b}
		return nil
	})
	return result, err
}
