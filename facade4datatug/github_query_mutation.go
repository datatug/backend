package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

var ErrGitHubQueryInvalid = errors.New("invalid GitHub query mutation")
var ErrGitHubQueryConflict = errors.New("GitHub query mutation conflict")
var ErrGitHubQueryOutcomeUncertain = errors.New("GitHub query mutation outcome uncertain")

type GitHubQueryFileChange struct {
	Path    string
	Content []byte
	Delete  bool
}
type GitHubQueryExpectedFile struct {
	OID  string
	Mode string
	Size int64
}
type GitHubQuerySavePlan struct {
	Changes      []GitHubQueryFileChange
	ExpectedTree map[string]GitHubQueryExpectedFile
	Response     dto.SaveQueryResponse
	BodyChanged  bool
}

type GitHubQueryRepository interface {
	Scope() GitHubCreateRepositoryScope
	PrepareQuerySave(context.Context, string, string, string, dto.SaveQueryRequest) (*GitHubQuerySavePlan, error)
	CurrentHead(context.Context, string) (string, error)
	CreateQueryCommit(context.Context, string, string, string, *GitHubQuerySavePlan) (string, error)
	FindQueryCommit(context.Context, string, string, string, *GitHubQuerySavePlan) (string, error)
}

// SaveGitHubQuery is the paid, actor-bound Cloud command. The provider commit
// is one CAS operation containing the metadata/body pair. A durable intent
// precedes that commit; retries prove the original commit before finalizing.
func (s *SharedProjectService) SaveGitHubQuery(ctx context.Context, actorID string, repositoryID int64, owner, name, folder string, request dto.SaveQueryRequest, repo GitHubQueryRepository) (*dto.SaveQueryResponse, error) {
	if s == nil || s.paid == nil || sharedProjectPortAbsent(repo) || actorID == "" || request.Validate() != nil || request.StoreID != models4datatug.GithubStoreID || request.ProjectID != models4datatug.NewGithubProjectID(owner, name, folder) || !gitHubCommitOID.MatchString(request.ExpectedBranchHead) || request.Branch == "" {
		return nil, ErrGitHubQueryInvalid
	}
	scope := repo.Scope()
	if scope.ActorID != actorID || scope.Permission != "write" || scope.RepositoryID != repositoryID || !strings.EqualFold(scope.Owner, owner) || !strings.EqualFold(scope.Name, name) {
		return nil, ErrSharedProjectUnauthorized
	}
	access, err := s.AuthorizeGitHubProjectWrite(ctx, actorID, repositoryID, owner, name, folder)
	if err != nil {
		return nil, err
	}
	digest, err := request.PayloadDigest()
	if err != nil {
		return nil, ErrGitHubQueryInvalid
	}
	plan, err := repo.PrepareQuerySave(ctx, folder, access.SharedProjectID, request.Branch, request)
	if err != nil {
		return nil, err
	}
	if plan == nil || len(plan.Changes) == 0 || plan.Response.Revision == "" {
		return nil, ErrGitHubQueryInvalid
	}
	queryPath := request.Query.ID
	if request.Query.FolderPath != "~" {
		queryPath = request.Query.FolderPath + "/" + queryPath
	}
	var committedHead string
	intentResult := plan.Response
	// A new accepted draft must be compared to the currently selected branch,
	// not merely an old commit supplied by the caller. Existing operations may
	// replay after their commit has advanced the branch.
	if s.recordQueryEditCandidates {
		previousRecord, _ := models4datatug.NewGitHubQueryOperationRecord(actorID, request.OperationID)
		if err := s.db.Get(ctx, previousRecord); err != nil && !record.IsNotFound(err) {
			return nil, err
		} else if !previousRecord.Exists() {
			currentHead, err := repo.CurrentHead(ctx, request.Branch)
			if err != nil {
				return nil, ErrGitHubQueryOutcomeUncertain
			}
			if currentHead != request.ExpectedBranchHead {
				// A concurrent request may have installed the same operation meanwhile.
				recheck, _ := models4datatug.NewGitHubQueryOperationRecord(actorID, request.OperationID)
				if err := s.db.Get(ctx, recheck); err != nil && !record.IsNotFound(err) {
					return nil, err
				} else if !recheck.Exists() {
					return nil, dto.ErrBranchHeadConflict
				}
			}
		}
	}
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		opRecord, op := models4datatug.NewGitHubQueryOperationRecord(actorID, request.OperationID)
		if err := tx.Get(txCtx, opRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if opRecord.Exists() {
			if op.Validate() != nil || op.ActorID != actorID || op.OperationID != request.OperationID || op.RequestDigest != digest || op.RepositoryID != repositoryID || op.Folder != folder || op.Branch != request.Branch || op.ExpectedHead != request.ExpectedBranchHead || op.QueryPath != queryPath || op.Result.Revision != plan.Response.Revision || op.Version == 2 && (op.SpaceID != access.SpaceID || op.ProjectID != access.SharedProjectID || op.BodyChanged != plan.BodyChanged) {
				return ErrGitHubQueryConflict
			}
			if op.Version == 2 && op.BodyChanged {
				candidateRecord, candidate := models4datatug.NewQueryEditCandidateRecord(op.SpaceID, actorID, request.OperationID)
				if err := tx.Get(txCtx, candidateRecord); err != nil || !queryEditCandidateMatchesOperation(*candidate, *op) {
					return ErrGitHubQueryConflict
				}
			}
			committedHead = op.CommittedHead
			return nil
		}
		// Project creation uses the same actor-wide client operation namespace.
		createRecord, _ := models4datatug.NewGitHubProjectCreateOperationRecord(actorID, request.OperationID)
		if err := tx.Get(txCtx, createRecord); err == nil {
			return ErrGitHubQueryConflict
		} else if !record.IsNotFound(err) {
			return err
		}
		acceptedAt := s.now().UTC()
		if s.recordQueryEditCandidates {
			if err := s.verifyGitHubQueryCandidateGrant(txCtx, tx, access, actorID, repositoryID, folder, acceptedAt); err != nil {
				return err
			}
		}
		*op = models4datatug.GitHubQueryOperation{Version: 1, ActorID: actorID, OperationID: request.OperationID, RequestDigest: digest, RepositoryID: repositoryID, Folder: folder, Branch: request.Branch, ExpectedHead: request.ExpectedBranchHead, QueryPath: queryPath, Result: intentResult, CreatedAt: acceptedAt}
		if s.recordQueryEditCandidates {
			op.Version = 2
			op.SpaceID, op.ProjectID, op.BodyChanged = access.SpaceID, access.SharedProjectID, plan.BodyChanged
		}
		if op.Validate() != nil {
			return ErrGitHubQueryInvalid
		}
		if err := tx.Insert(txCtx, opRecord); err != nil {
			return err
		}
		if !s.recordQueryEditCandidates || !plan.BodyChanged {
			return nil
		}
		candidateRecord, candidate := models4datatug.NewQueryEditCandidateRecord(access.SpaceID, actorID, request.OperationID)
		*candidate = models4datatug.QueryEditCandidate{Version: 1, SourceID: models4datatug.GitHubQueryEditSourceID, EventID: models4datatug.NewQueryEditCandidateID(actorID, request.OperationID), ActorID: actorID, OperationID: request.OperationID, SpaceID: access.SpaceID, ProjectID: access.SharedProjectID, RepositoryID: repositoryID, Folder: folder, ExpectedHead: request.ExpectedBranchHead, QueryPath: queryPath, BodyChanged: true, Classification: models4datatug.QueryEditUnclassified, AcceptedAtUTC: acceptedAt}
		if candidate.Validate() != nil {
			return ErrGitHubQueryInvalid
		}
		return tx.Insert(txCtx, candidateRecord)
	})
	if err != nil {
		return nil, err
	}
	marker := githubQueryMarker(actorID, request.OperationID, digest)
	if committedHead == "" {
		committedHead, err = repo.FindQueryCommit(ctx, request.Branch, request.ExpectedBranchHead, marker, plan)
		if err != nil {
			return nil, ErrGitHubQueryOutcomeUncertain
		}
	}
	if committedHead == "" {
		currentHead, err := repo.CurrentHead(ctx, request.Branch)
		if err != nil {
			return nil, ErrGitHubQueryOutcomeUncertain
		}
		if currentHead != request.ExpectedBranchHead {
			return nil, dto.ErrBranchHeadConflict
		}
		committedHead, err = repo.CreateQueryCommit(ctx, request.Branch, request.ExpectedBranchHead, "Save DataTug query; DataTug-Operation: "+marker, plan)
		if err != nil {
			committedHead, _ = repo.FindQueryCommit(ctx, request.Branch, request.ExpectedBranchHead, marker, plan)
			if committedHead == "" {
				return nil, ErrGitHubQueryOutcomeUncertain
			}
		}
	}
	if !gitHubCommitOID.MatchString(committedHead) || committedHead == request.ExpectedBranchHead {
		return nil, ErrGitHubQueryOutcomeUncertain
	}
	if err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		opRecord, op := models4datatug.NewGitHubQueryOperationRecord(actorID, request.OperationID)
		if err := tx.Get(txCtx, opRecord); err != nil || op.Validate() != nil || op.RequestDigest != digest || op.RepositoryID != repositoryID || op.QueryPath != queryPath {
			return ErrGitHubQueryConflict
		}
		if op.CommittedHead != "" && op.CommittedHead != committedHead {
			return ErrGitHubQueryConflict
		}
		if op.CommittedHead == committedHead {
			return nil
		}
		return tx.Update(txCtx, opRecord.Key(), []update.Update{update.ByFieldPath([]string{"committedHead"}, committedHead)})
	}); err != nil {
		return nil, fmt.Errorf("query commit recorded remotely; receipt reconciliation required: %w", err)
	}
	_ = access // the current member/paid proof was required before both first write and replay.
	response := intentResult
	response.BranchHead = committedHead
	return &response, nil
}

func queryEditCandidateMatchesOperation(candidate models4datatug.QueryEditCandidate, op models4datatug.GitHubQueryOperation) bool {
	return candidate.Validate() == nil && op.Version == 2 && op.BodyChanged && candidate.ActorID == op.ActorID && candidate.OperationID == op.OperationID && candidate.SpaceID == op.SpaceID && candidate.ProjectID == op.ProjectID && candidate.RepositoryID == op.RepositoryID && candidate.Folder == op.Folder && candidate.ExpectedHead == op.ExpectedHead && candidate.QueryPath == op.QueryPath && candidate.AcceptedAtUTC.Equal(op.CreatedAt)
}

// The outer authorization protects the provider request; this same-transaction
// read protects the durable accepted-draft fact against a changed project,
// member, or paid grant between that read and intent insertion.
func (s *SharedProjectService) verifyGitHubQueryCandidateGrant(ctx context.Context, tx dal.ReadTransaction, access GitHubProjectAccess, actorID string, repositoryID int64, folder string, at time.Time) error {
	if s.ownerLinks == nil || s.paid == nil {
		return ErrSharedProjectUnavailable
	}
	locatorRecord, locator := models4datatug.NewGitHubProjectLocatorRecord(repositoryID, folder)
	if err := tx.Get(ctx, locatorRecord); err != nil {
		if record.IsNotFound(err) {
			return ErrSharedProjectUnauthorized
		}
		return err
	}
	if locator.Validate() != nil || locator.Status != models4datatug.GitHubProjectReady || locator.SpaceID != access.SpaceID || locator.ProjectID != access.SharedProjectID {
		return ErrSharedProjectUnauthorized
	}
	projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(access.SpaceID, access.SharedProjectID)
	if err := tx.Get(ctx, projectRecord); err != nil {
		if record.IsNotFound(err) {
			return ErrSharedProjectUnauthorized
		}
		return err
	}
	if project.Status != models4datatug.GitHubProjectReady || project.Storage != models4datatug.GithubStoreID || project.GitHub == nil || *project.GitHub != access.Binding || project.GitHub.RepositoryID != repositoryID || project.GitHub.Folder != folder {
		return ErrSharedProjectUnauthorized
	}
	ref := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(access.SpaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: access.SharedProjectID}}
	admission, err := readCurrentProjectMember(ctx, tx, *s.ownerLinks, ref, project, actorID)
	if err != nil {
		return err
	}
	_, err = readCurrentPaidProjectAccess(ctx, tx, *s.paid, admission.ActorID, admission.PayerID, at)
	return err
}

func githubQueryMarker(actorID, operationID, digest string) string {
	encoded, _ := json.Marshal([4]string{"github-query-save/1", actorID, operationID, digest})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
