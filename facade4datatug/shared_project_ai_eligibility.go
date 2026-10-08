// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

var ErrProjectAIEligibilityUnavailable = errors.New("project AI eligibility unavailable")

// ProjectAIEligibility is a minimal, server-computed projection. Actor
// membership and sponsor entitlement are proved separately before it is
// returned; no payer or subscription identifiers leave this package.
type ProjectAIEligibility struct {
	AIAllowed bool   `json:"aiAllowed"`
	Reason    string `json:"reason,omitempty"`
}

// ReadSharedProjectAIEligibility authorizes the current linked project reader
// and then evaluates the payer recorded by that project's immutable admission.
// The client supplies a project locator only; it cannot select the payer.
func (s *SharedProjectService) ReadSharedProjectAIEligibility(ctx context.Context, actorID, spaceID, projectID string) (ProjectAIEligibility, error) {
	if s == nil || s.paid == nil || s.ownerLinks == nil || sharedProjectPortAbsent(s.ownerLinks.contacts) || sharedProjectPortAbsent(s.db) || s.now == nil || sharedProjectPortAbsent(ctx) || actorID == "" || models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(projectID) != nil {
		return ProjectAIEligibility{}, ErrProjectAIEligibilityUnavailable
	}
	return s.readProjectAIEligibility(ctx, actorID, spaceID, projectID)
}

// ReadGitHubProjectAIEligibility binds the entitlement read to the registered
// immutable GitHub locator. The HTTP layer separately obtains fresh GitHub
// read permission for the exact repository before calling this method.
func (s *SharedProjectService) ReadGitHubProjectAIEligibility(ctx context.Context, actorID string, repositoryID int64, owner, name, folder string) (ProjectAIEligibility, error) {
	if s == nil || s.paid == nil || s.ownerLinks == nil || sharedProjectPortAbsent(s.ownerLinks.contacts) || sharedProjectPortAbsent(s.db) || s.now == nil || sharedProjectPortAbsent(ctx) || actorID == "" || repositoryID < 1 || owner == "" || name == "" || folder == "" {
		return ProjectAIEligibility{}, ErrProjectAIEligibilityUnavailable
	}
	at := s.now().UTC()
	if at.IsZero() {
		return ProjectAIEligibility{}, ErrProjectAIEligibilityUnavailable
	}
	var result ProjectAIEligibility
	err := s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		locatorRecord, locator := models4datatug.NewGitHubProjectLocatorRecord(repositoryID, folder)
		if err := tx.Get(txCtx, locatorRecord); err != nil {
			return ErrSharedProjectUnauthorized
		}
		if locator.Validate() != nil || locator.Status != models4datatug.GitHubProjectReady {
			return ErrSharedProjectConflict
		}
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(locator.SpaceID, locator.ProjectID)
		if err := tx.Get(txCtx, projectRecord); err != nil {
			return err
		}
		binding := project.GitHub
		if binding == nil || binding.Validate() != nil || binding.RepositoryID != repositoryID || binding.Folder != folder || !strings.EqualFold(binding.Owner, owner) || !strings.EqualFold(binding.Name, name) || project.Storage != models4datatug.GithubStoreID || project.Status != models4datatug.GitHubProjectReady {
			return ErrSharedProjectUnauthorized
		}
		ref := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(locator.SpaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: locator.ProjectID}}
		admission, err := readCurrentProjectMember(txCtx, tx, *s.ownerLinks, ref, project, actorID)
		if err != nil {
			return err
		}
		if err := verifyGitHubAdmissionSource(txCtx, tx, admission, project); err != nil {
			return err
		}
		result, err = s.projectAIEligibilityForAdmission(txCtx, tx, *s.paid, admission, at)
		return err
	})
	if err != nil {
		return ProjectAIEligibility{}, err
	}
	return result, nil
}

func (s *SharedProjectService) readProjectAIEligibility(ctx context.Context, actorID, spaceID, projectID string) (ProjectAIEligibility, error) {
	at := s.now().UTC()
	if at.IsZero() || s.ownerLinks == nil || sharedProjectPortAbsent(s.ownerLinks.contacts) {
		return ProjectAIEligibility{}, ErrProjectAIEligibilityUnavailable
	}
	var result ProjectAIEligibility
	err := s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(spaceID, projectID)
		if err := tx.Get(txCtx, projectRecord); err != nil {
			return err
		}
		if project.Storage == models4datatug.GithubStoreID || project.GitHub != nil {
			return ErrSharedProjectUnauthorized
		}
		if project.Access != models4datatug.AccessProtected || len(project.UserIDs) != 0 {
			return ErrSharedProjectUnauthorized
		}
		ref := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(spaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: projectID}}
		admission, err := readCurrentProjectMember(txCtx, tx, *s.ownerLinks, ref, project, actorID)
		if err != nil {
			return err
		}
		result, err = s.projectAIEligibilityForAdmission(txCtx, tx, *s.paid, admission, at)
		return err
	})
	if err != nil {
		return ProjectAIEligibility{}, err
	}
	return result, nil
}

func (s *SharedProjectService) projectAIEligibilityForAdmission(ctx context.Context, tx dal.ReadTransaction, options PaidSharedProjectOptions, admission *models4datatug.ProjectAdmission, at time.Time) (ProjectAIEligibility, error) {
	if admission == nil {
		return ProjectAIEligibility{}, ErrProjectAIEligibilityUnavailable
	}
	creator, err := options.Directory.PersonalAccount(ctx, admission.ActorID)
	if err != nil || creator.ID != admission.PayerID || creator.Title == "" {
		return ProjectAIEligibility{}, ErrPlanEffectUnproved
	}
	entitled, err := readCurrentPaidSponsorAccess(ctx, tx, options, admission.PayerID, at)
	if err != nil {
		return ProjectAIEligibility{}, err
	}
	if !entitled {
		return ProjectAIEligibility{AIAllowed: false, Reason: "plan_ended"}, nil
	}
	return ProjectAIEligibility{AIAllowed: true}, nil
}

func readCurrentProjectMember(ctx context.Context, tx dal.ReadTransaction, links sharedProjectOwnerLinks, ref contract4linkage.RelationshipEntityRef, project *models4datatug.SharedLinkedProject, actorID string) (*models4datatug.ProjectAdmission, error) {
	if actorID == "" || project == nil || project.Access != models4datatug.AccessProtected || len(project.UserIDs) != 0 || sharedProjectPortAbsent(links.contacts) {
		return nil, ErrSharedProjectUnauthorized
	}
	admission, err := readLinkedProjectAdmission(ctx, tx, ref, project)
	if err != nil || admission == nil {
		return nil, ErrProjectAIEligibilityUnavailable
	}
	rolesByContact, err := readProjectContactRoles(ref.SpaceID, project.WithRelatedAndIDs, links.catalog)
	if err != nil {
		return nil, err
	}
	for contactRef, roles := range rolesByContact {
		if len(roles) == 0 || contactRef.SpaceID != ref.SpaceID {
			continue
		}
		state, err := links.contacts.ReadCurrentProjectContact(ctx, sharedProjectReadTransaction{tx}, contactRef)
		if err != nil {
			return nil, ErrProjectAIEligibilityUnavailable
		}
		if state.Ref != contactRef || !state.Exists || !state.Active || state.UserID != actorID {
			continue
		}
		contactRecord, graph := models4datatug.NewProjectContactLinkageRecord(contactRef)
		if err := tx.Get(ctx, contactRecord); err != nil || graph.Validate() != nil {
			return nil, ErrProjectAIEligibilityUnavailable
		}
		edge, err := graphItem(*graph, contactRef.SpaceID, ref)
		if err != nil || !slices.Equal(rolesOf(edge, false), roles) {
			return nil, ErrProjectAIEligibilityUnavailable
		}
		return admission, nil
	}
	return nil, ErrSharedProjectUnauthorized
}

func verifyGitHubAdmissionSource(ctx context.Context, tx dal.ReadTransaction, admission *models4datatug.ProjectAdmission, project *models4datatug.SharedLinkedProject) error {
	if admission == nil {
		return ErrProjectAIEligibilityUnavailable
	}
	receiptRecord, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(admission.SpaceID, admission.CommandID)
	if err := tx.Get(ctx, receiptRecord); err != nil || receipt.GitHub == nil || project.GitHub == nil || receipt.GitHub.Binding != *project.GitHub {
		return ErrProjectAIEligibilityUnavailable
	}
	return nil
}

// readCurrentPaidSponsorAccess proves the plan attached to a project's payer
// without requiring the current project reader to be that payer. It does not
// weaken the existing mutation authorization path.
func readCurrentPaidSponsorAccess(ctx context.Context, tx dal.ReadTransaction, o PaidSharedProjectOptions, payer string, at time.Time) (bool, error) {
	if ctx == nil || sharedProjectPortAbsent(tx) || o.validate() != nil || models4datatug.ValidateSharedProjectIdentifier(payer) != nil || at.IsZero() {
		return false, ErrSharedProjectUnavailable
	}
	f, err := o.Owner.ReadOwner(ctx, tx, o.Mode, o.Product, payer)
	if err != nil {
		return false, err
	}
	if f.Mode != o.Mode || f.Family != o.Product || f.AccountID != payer || f.OwnerSubscriptionID == "" || f.OwnerGeneration < 1 || f.SubscriptionRevision < 1 {
		return false, ErrPlanEffectUnproved
	}
	app, exists, err := readPlanApplication(ctx, tx, f)
	if err != nil {
		return false, err
	}
	if !exists || app.V != 1 || app.Mode != f.Mode || app.Family != f.Family || app.AccountID != f.AccountID || app.OwnerSubscriptionID != f.OwnerSubscriptionID || app.OwnerGeneration != f.OwnerGeneration || app.SubscriptionRevision != f.SubscriptionRevision || app.LastProSubscriptionID != f.OwnerSubscriptionID || app.LastProOwnerGeneration != f.OwnerGeneration || (app.LastProPlanID != "datatug-pro-monthly" && app.LastProPlanID != "datatug-pro-annual") || app.LastProQuoteKey == "" || app.LastProPaidServiceProofID == "" || app.LimitsVersion == "" || app.LastProProtectedProjects <= 0 || app.LastProProtectedProjectUsers <= 0 {
		return false, ErrPlanEffectUnproved
	}
	plan, exists, err := readPublicPlan(ctx, tx, f)
	if err != nil {
		return false, err
	}
	if !exists || plan.V != 1 {
		return false, ErrPlanEffectUnproved
	}
	if plan.Plan == "free" && plan.Status == "ended" && plan.Period == "none" && !plan.Founding && plan.Limits == nil && knownPlanEndReason(plan.EndedReason) {
		return false, nil
	}
	if plan.Limits == nil || plan.Limits.ProtectedProjects == nil || plan.Limits.ProtectedProjectUsers == nil || *plan.Limits.ProtectedProjects != app.LastProProtectedProjects || *plan.Limits.ProtectedProjectUsers != app.LastProProtectedProjectUsers {
		return false, ErrPlanEffectUnproved
	}
	if plan.Plan != "pro" || !effectiveProForPurchase(plan, o.Config, at) || plan.PaidUntil == nil || !at.Before(*plan.PaidUntil) {
		return false, nil
	}
	limits, err := resolvedProLimits(*plan.Limits, o.Config.ProLimits)
	if err != nil || limits.ProtectedProjects == nil || limits.ProtectedProjectUsers == nil || validateLimits(limits) != nil || validateModels(o.Config.ProModels, limits) != nil || plan.AIExtraQuestions < 0 || limits.AIQuestions > math.MaxInt64-plan.AIExtraQuestions {
		return false, ErrPlanEffectUnproved
	}
	return true, nil
}

func knownPlanEndReason(reason string) bool {
	switch reason {
	case "unpaid", "paused", "canceled", "refunded":
		return true
	default:
		return false
	}
}
