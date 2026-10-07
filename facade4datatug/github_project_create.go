package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/backend/template4datatug"
)

var (
	ErrGitHubProjectConflict  = errors.New("GitHub project create conflict")
	ErrGitHubOutcomeUncertain = errors.New("GitHub project create outcome uncertain")
	ErrGitHubProjectInvalid   = errors.New("invalid GitHub project create command")
	gitHubCommitOID           = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
)

// GitHubCreateRepository is implemented by a per-request, actor-bound adapter
// around githubauth4datatug.AuthorizedGitHubRepository. Its methods never
// expose OAuth or installation tokens to this module or to a caller.
type GitHubCreateRepository interface {
	Scope() GitHubCreateRepositoryScope
	CurrentHead(context.Context, string) (string, error)
	EnsureFolderEmpty(context.Context, string, string) error
	CreateFilesCommit(context.Context, string, string, string, map[string][]byte) (string, error)
	FindCommitByMarker(context.Context, string, string, string, map[string][]byte) (string, error)
}

type GitHubCreateRepositoryScope struct {
	ActorID, Owner, Name, Permission string
	RepositoryID                     int64
}

type GitHubProjectCreateCommand struct {
	ActorID, SpaceID, OperationID, Title string
	Source                               models4datatug.GitHubCreateSource
}

func (c GitHubProjectCreateCommand) Validate() error {
	if (SharedProjectCreateCommand{ActorID: c.ActorID, SpaceID: c.SpaceID, CommandID: c.OperationID, Title: c.Title}).Validate() != nil || c.Source.Binding.Validate() != nil || !gitHubCommitOID.MatchString(c.Source.ExpectedHead) || c.Source.TemplateID != template4datatug.DemoProjectID || c.Source.TemplateCommit != template4datatug.DemoProjectCommit {
		return ErrGitHubProjectInvalid
	}
	if template4datatug.ValidateFolder(c.Source.Binding.Folder) != nil {
		return ErrGitHubProjectInvalid
	}
	return nil
}

type GitHubProjectCreateResult struct {
	ID              string `json:"id"`
	Storage         string `json:"storage"`
	Project         string `json:"project"`
	SpaceID         string `json:"spaceID"`
	SharedProjectID string `json:"sharedProjectID"`
	Branch          string `json:"branch"`
	BranchHead      string `json:"branchHead"`
	Revision        string `json:"revision"`
	TemplateID      string `json:"templateID"`
	TemplateCommit  string `json:"templateCommit"`
}

type githubCreateReservation struct {
	projectID string
	createdAt time.Time
	readyHead string
}

// CreateGitHubProject reserves paid shared-project capacity and owner linkage
// before provider mutation, then commits the exact pinned files with a branch
// head precondition. A failed/unknown provider result leaves the hidden intent
// and quota reserved for safe same-operation recovery; it never creates a
// second commit or silently frees capacity.
func (s *SharedProjectService) CreateGitHubProject(ctx context.Context, command GitHubProjectCreateCommand, repo GitHubCreateRepository) (GitHubProjectCreateResult, error) {
	var zero GitHubProjectCreateResult
	if s == nil || s.paid == nil || s.ownerLinks == nil || sharedProjectPortAbsent(repo) || command.Validate() != nil {
		return zero, ErrGitHubProjectInvalid
	}
	scope := repo.Scope()
	binding := command.Source.Binding
	if scope.ActorID != command.ActorID || scope.Permission != "write" || scope.RepositoryID != binding.RepositoryID || !strings.EqualFold(scope.Owner, binding.Owner) || !strings.EqualFold(scope.Name, binding.Name) {
		return zero, ErrSharedProjectUnauthorized
	}
	// A read-only preflight avoids a quota reservation for an already occupied
	// folder. A CAS at the provider still fences races after this observation.
	operationRecord, prior := models4datatug.NewGitHubProjectCreateOperationRecord(command.ActorID, command.OperationID)
	readErr := s.db.Get(ctx, operationRecord)
	if readErr != nil && !record.IsNotFound(readErr) {
		return zero, readErr
	}
	if record.IsNotFound(readErr) {
		head, err := repo.CurrentHead(ctx, binding.Branch)
		if err != nil {
			return zero, err
		}
		if head != command.Source.ExpectedHead {
			return zero, ErrGitHubProjectConflict
		}
		if err := repo.EnsureFolderEmpty(ctx, head, binding.Folder); err != nil {
			return zero, err
		}
	} else if prior.ActorID != command.ActorID || prior.OperationID != command.OperationID {
		return zero, ErrGitHubProjectConflict
	}
	account, err := ResolvePersonalPayer(ctx, command.ActorID, "", s.paid.Directory)
	if err != nil || models4datatug.ValidateSharedProjectIdentifier(account.ID) != nil {
		return zero, ErrSharedProjectUnauthorized
	}
	digest := models4datatug.GitHubSharedProjectCreateDigest(command.ActorID, command.SpaceID, command.OperationID, command.Title, account.ID, s.paid.Mode, s.paid.Product, command.Source)
	if readErr == nil && prior.Match(command.ActorID, command.OperationID, command.SpaceID, digest) != nil {
		return zero, ErrGitHubProjectConflict
	}
	createBinding := SharedProjectCreateBinding{
		ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.OperationID, RequestDigest: digest,
		PayerID: account.ID, Mode: s.paid.Mode, Product: s.paid.Product,
	}
	prepared, err := s.authority.PrepareSharedProjectCreate(ctx, createBinding)
	if err != nil || prepared.Binding != createBinding || prepared.IssuedAt.IsZero() || sharedProjectPortAbsent(prepared.Validator) {
		return zero, ErrSharedProjectUnauthorized
	}
	observedAt := s.now().UTC()
	if observedAt.IsZero() || observedAt.Before(prepared.IssuedAt) {
		return zero, ErrSharedProjectUnauthorized
	}
	reserved, err := s.reserveGitHubCreate(ctx, command, createBinding, prepared, observedAt)
	if err != nil {
		return zero, err
	}
	if reserved.readyHead != "" {
		return githubCreateResult(command, reserved.projectID, reserved.readyHead), nil
	}
	files, err := template4datatug.CloneDemoProject(binding.Folder, reserved.projectID, command.Title, reserved.createdAt)
	if err != nil {
		return zero, err
	}
	marker := githubCreateMarker(command.ActorID, command.OperationID, digest)
	committedHead, err := repo.FindCommitByMarker(ctx, binding.Branch, command.Source.ExpectedHead, marker, files)
	if err != nil {
		return zero, ErrGitHubOutcomeUncertain
	}
	if committedHead == "" {
		head, err := repo.CurrentHead(ctx, binding.Branch)
		if err != nil || head != command.Source.ExpectedHead {
			return zero, ErrGitHubOutcomeUncertain
		}
		committedHead, err = repo.CreateFilesCommit(ctx, binding.Branch, command.Source.ExpectedHead, "Create DataTug project\n\nDataTug-Operation: "+marker, files)
		if err != nil {
			// The provider can commit and lose the response. Recover only a
			// verified marker in bounded ancestry of the selected branch.
			committedHead, _ = repo.FindCommitByMarker(ctx, binding.Branch, command.Source.ExpectedHead, marker, files)
			if committedHead == "" {
				return zero, ErrGitHubOutcomeUncertain
			}
		}
	}
	if !gitHubCommitOID.MatchString(committedHead) || committedHead == command.Source.ExpectedHead {
		return zero, ErrGitHubOutcomeUncertain
	}
	if err := s.finishGitHubCreate(ctx, command, createBinding, reserved.projectID, committedHead); err != nil {
		return zero, err
	}
	return githubCreateResult(command, reserved.projectID, committedHead), nil
}

func githubCreateMarker(actorID, operationID, digest string) string {
	b, _ := json.Marshal([4]string{"github-project-create/1", actorID, operationID, digest})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func githubCreateResult(c GitHubProjectCreateCommand, projectID, committedHead string) GitHubProjectCreateResult {
	externalID := models4datatug.NewGithubProjectID(c.Source.Binding.Owner, c.Source.Binding.Name, c.Source.Binding.Folder)
	return GitHubProjectCreateResult{
		ID: externalID, Storage: models4datatug.GithubStoreID, Project: externalID, SpaceID: c.SpaceID,
		SharedProjectID: projectID, Branch: c.Source.Binding.Branch, BranchHead: committedHead,
		Revision: committedHead, TemplateID: c.Source.TemplateID, TemplateCommit: c.Source.TemplateCommit,
	}
}

func (s *SharedProjectService) reserveGitHubCreate(ctx context.Context, command GitHubProjectCreateCommand, binding SharedProjectCreateBinding, prepared PreparedSharedProjectCreate, observedAt time.Time) (githubCreateReservation, error) {
	var result githubCreateReservation
	newID, idErr := s.ids.NewID(ctx)
	if idErr == nil && models4datatug.ValidateSharedProjectIdentifier(newID) != nil {
		idErr = ErrSharedProjectUnavailable
	}
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		if err := prepared.Validator.ValidateSharedProjectCreateInTransaction(txCtx, tx, binding, observedAt); err != nil {
			return ErrSharedProjectUnauthorized
		}
		paidAt := s.now().UTC()
		if paidAt.IsZero() || paidAt.Before(observedAt) {
			return ErrSharedProjectUnauthorized
		}
		admission, err := s.readPaidAdmission(txCtx, tx, binding, paidAt)
		if err != nil {
			return err
		}
		opRecord, op := models4datatug.NewGitHubProjectCreateOperationRecord(command.ActorID, command.OperationID)
		if err := tx.Get(txCtx, opRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if opRecord.Exists() {
			if op.Match(command.ActorID, command.OperationID, command.SpaceID, binding.RequestDigest) != nil || op.Binding != command.Source.Binding || op.ExpectedHead != command.Source.ExpectedHead || op.TemplateID != command.Source.TemplateID || op.TemplateCommit != command.Source.TemplateCommit {
				return ErrGitHubProjectConflict
			}
			if err := admission.verifyReplay(txCtx, tx, binding, op.ProjectID); err != nil {
				return err
			}
			rr, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(command.SpaceID, command.OperationID)
			if err := tx.Get(txCtx, rr); err != nil || receipt.Validate() != nil || receipt.Version != 2 || receipt.RequestDigest != binding.RequestDigest || receipt.ProjectID != op.ProjectID || receipt.GitHub == nil || *receipt.GitHub != command.Source {
				return ErrGitHubProjectConflict
			}
			pr, project := models4datatug.NewSharedLinkedProjectRecord(command.SpaceID, op.ProjectID)
			if err := tx.Get(txCtx, pr); err != nil || project.GitHub == nil || *project.GitHub != command.Source.Binding || project.Storage != models4datatug.GithubStoreID || project.Status != op.Status || project.Access != models4datatug.AccessProtected {
				return ErrGitHubProjectConflict
			}
			lr, locator := models4datatug.NewGitHubProjectLocatorRecord(op.Binding.RepositoryID, op.Binding.Folder)
			if err := tx.Get(txCtx, lr); err != nil || locator.Validate() != nil || locator.SpaceID != command.SpaceID || locator.ProjectID != op.ProjectID || locator.Status != op.Status {
				return ErrGitHubProjectConflict
			}
			result = githubCreateReservation{projectID: op.ProjectID, createdAt: op.CreatedAt, readyHead: op.CommittedHead}
			return nil
		}
		if idErr != nil {
			return fmt.Errorf("generate GitHub project ID: %w", idErr)
		}
		locatorRecord, locator := models4datatug.NewGitHubProjectLocatorRecord(command.Source.Binding.RepositoryID, command.Source.Binding.Folder)
		if err := tx.Get(txCtx, locatorRecord); err == nil {
			return ErrGitHubProjectConflict
		} else if !record.IsNotFound(err) {
			return err
		}
		receiptRecord, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(command.SpaceID, command.OperationID)
		if err := tx.Get(txCtx, receiptRecord); err == nil {
			return ErrGitHubProjectConflict
		} else if !record.IsNotFound(err) {
			return err
		}
		if err := admission.readNewAllocation(txCtx, tx, binding, newID); err != nil {
			return err
		}
		ownerPlan, err := s.prepareProjectOwnerLink(txCtx, tx, binding, newID, observedAt)
		if err != nil {
			return err
		}
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(command.SpaceID, newID)
		project.WithRelatedAndIDs = ownerPlan.graph
		project.Title = command.Title
		project.Access = models4datatug.AccessProtected
		project.Created = &models4datatug.Created{At: observedAt}
		project.Storage = models4datatug.GithubStoreID
		project.Status = models4datatug.GitHubProjectInitializing
		project.GitHub = &command.Source.Binding
		*receipt = models4datatug.SharedProjectCreateReceipt{
			Version: 2, ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.OperationID,
			Title: command.Title, RequestDigest: binding.RequestDigest, ProjectID: newID, CreatedAt: observedAt,
			PayerID: binding.PayerID, Mode: binding.Mode, Product: binding.Product, OwnerContact: ownerPlan.proof,
			GitHub: &command.Source,
		}
		*locator = models4datatug.GitHubProjectLocator{
			Version: 1, RepositoryID: command.Source.Binding.RepositoryID, Folder: command.Source.Binding.Folder,
			SpaceID: command.SpaceID, ProjectID: newID, Status: models4datatug.GitHubProjectInitializing, CreatedAt: observedAt,
		}
		*op = models4datatug.GitHubProjectCreateOperation{
			Version: 1, ActorID: command.ActorID, OperationID: command.OperationID, SpaceID: command.SpaceID,
			ProjectID: newID, RequestDigest: binding.RequestDigest, Binding: command.Source.Binding,
			ExpectedHead: command.Source.ExpectedHead, TemplateID: command.Source.TemplateID,
			TemplateCommit: command.Source.TemplateCommit, Status: models4datatug.GitHubProjectInitializing, CreatedAt: observedAt,
		}
		for _, item := range []record.Record{projectRecord, receiptRecord, locatorRecord, opRecord} {
			if err := tx.Insert(txCtx, item); err != nil {
				return err
			}
		}
		if err := admission.writeAllocation(txCtx, tx, binding, newID, observedAt, ownerPlan.proof); err != nil {
			return err
		}
		if err := ownerPlan.writeContact(txCtx, tx); err != nil {
			return err
		}
		result = githubCreateReservation{projectID: newID, createdAt: observedAt}
		return nil
	})
	return result, err
}

func (s *SharedProjectService) finishGitHubCreate(ctx context.Context, command GitHubProjectCreateCommand, binding SharedProjectCreateBinding, projectID, committedHead string) error {
	return s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		opRecord, op := models4datatug.NewGitHubProjectCreateOperationRecord(command.ActorID, command.OperationID)
		if err := tx.Get(txCtx, opRecord); err != nil || op.Match(command.ActorID, command.OperationID, command.SpaceID, binding.RequestDigest) != nil || op.ProjectID != projectID {
			return ErrGitHubProjectConflict
		}
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(command.SpaceID, projectID)
		if err := tx.Get(txCtx, projectRecord); err != nil || project.GitHub == nil || *project.GitHub != command.Source.Binding || project.Storage != models4datatug.GithubStoreID {
			return ErrGitHubProjectConflict
		}
		locatorRecord, locator := models4datatug.NewGitHubProjectLocatorRecord(command.Source.Binding.RepositoryID, command.Source.Binding.Folder)
		if err := tx.Get(txCtx, locatorRecord); err != nil || locator.Validate() != nil || locator.SpaceID != command.SpaceID || locator.ProjectID != projectID {
			return ErrGitHubProjectConflict
		}
		receiptRecord, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(command.SpaceID, command.OperationID)
		if err := tx.Get(txCtx, receiptRecord); err != nil || receipt.Validate() != nil || receipt.RequestDigest != binding.RequestDigest || receipt.ProjectID != projectID {
			return ErrGitHubProjectConflict
		}
		admissionRecord, admission := models4datatug.NewProjectAdmissionRecord(command.SpaceID, projectID)
		if err := tx.Get(txCtx, admissionRecord); err != nil || admission.Validate() != nil || admission.RequestDigest != binding.RequestDigest || admission.ProjectID != projectID {
			return ErrGitHubProjectConflict
		}
		userIndexRecord, userIndex, indexExists, err := readProjectIndex(txCtx, tx, command.ActorID)
		if err != nil {
			return err
		}
		if op.Status == models4datatug.GitHubProjectReady {
			if op.CommittedHead != committedHead || project.Status != models4datatug.GitHubProjectReady || locator.Status != models4datatug.GitHubProjectReady {
				return ErrGitHubProjectConflict
			}
			return nil
		}
		if op.Status != models4datatug.GitHubProjectInitializing || project.Status != models4datatug.GitHubProjectInitializing || locator.Status != models4datatug.GitHubProjectInitializing || op.CommittedHead != "" {
			return ErrGitHubProjectConflict
		}
		if err := tx.Update(txCtx, opRecord.Key(), []update.Update{
			update.ByFieldPath([]string{"status"}, models4datatug.GitHubProjectReady),
			update.ByFieldPath([]string{"committedHead"}, committedHead),
		}); err != nil {
			return err
		}
		if err := tx.Update(txCtx, projectRecord.Key(), []update.Update{update.ByFieldPath([]string{"status"}, models4datatug.GitHubProjectReady)}); err != nil {
			return err
		}
		if err := tx.Update(txCtx, locatorRecord.Key(), []update.Update{update.ByFieldPath([]string{"status"}, models4datatug.GitHubProjectReady)}); err != nil {
			return err
		}
		externalID := models4datatug.NewGithubProjectID(command.Source.Binding.Owner, command.Source.Binding.Name, command.Source.Binding.Folder)
		return writeProjectBrief(txCtx, tx, userIndexRecord, userIndex, indexExists,
			storeIndex{ID: models4datatug.GithubStoreID, Type: models4datatug.GithubStoreType, Title: models4datatug.GithubStoreTitle},
			externalID, &models4datatug.ProjectBrief{
				Title: command.Title, Access: models4datatug.AccessProtected, ProjectAPI: "cloud", Branch: command.Source.Binding.Branch,
			})
	})
}
