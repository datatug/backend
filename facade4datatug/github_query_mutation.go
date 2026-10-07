package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
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
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		opRecord, op := models4datatug.NewGitHubQueryOperationRecord(actorID, request.OperationID)
		if err := tx.Get(txCtx, opRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if opRecord.Exists() {
			if op.Validate() != nil || op.ActorID != actorID || op.OperationID != request.OperationID || op.RequestDigest != digest || op.RepositoryID != repositoryID || op.Folder != folder || op.Branch != request.Branch || op.ExpectedHead != request.ExpectedBranchHead || op.QueryPath != queryPath || op.Result.Revision != plan.Response.Revision {
				return ErrGitHubQueryConflict
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
		*op = models4datatug.GitHubQueryOperation{Version: 1, ActorID: actorID, OperationID: request.OperationID, RequestDigest: digest, RepositoryID: repositoryID, Folder: folder, Branch: request.Branch, ExpectedHead: request.ExpectedBranchHead, QueryPath: queryPath, Result: intentResult, CreatedAt: s.now().UTC()}
		if op.Validate() != nil {
			return ErrGitHubQueryInvalid
		}
		return tx.Insert(txCtx, opRecord)
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

func githubQueryMarker(actorID, operationID, digest string) string {
	encoded, _ := json.Marshal([4]string{"github-query-save/1", actorID, operationID, digest})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
