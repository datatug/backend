// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
)

const businessProjectAdmissionProfileVersion = "business-shared-project-v1"

func readNewBusinessProjectAllocation(ctx context.Context, tx dal.ReadwriteTransaction, binding SharedProjectCreateBinding, projectID string) error {
	if !isBusinessProjectBinding(binding) {
		return ErrSharedProjectUnauthorized
	}
	r, _ := models4datatug.NewProjectAdmissionRecord(binding.SpaceID, projectID)
	err := tx.Get(ctx, r)
	if err == nil {
		return ErrSharedProjectConflict
	}
	if !record.IsNotFound(err) {
		return err
	}
	return nil
}

func writeBusinessProjectAdmission(ctx context.Context, tx dal.ReadwriteTransaction, binding SharedProjectCreateBinding, projectID string, at time.Time, owner models4datatug.ProjectOwnerContactProof, access SpaceServiceAccess) error {
	if !isBusinessProjectBinding(binding) || access.Mode != binding.Mode || access.ServiceID != BusinessProjectServiceID || access.PayerSpaceID != binding.SpaceID || access.ProductID != binding.Product || access.State != "active" || access.GrantVersion == "" || !access.UnlimitedProjects || !access.UnlimitedContacts || !at.Before(access.PaidUntilUTC) {
		return ErrSharedProjectUnauthorized
	}
	r, admission := models4datatug.NewProjectAdmissionRecord(binding.SpaceID, projectID)
	*admission = models4datatug.ProjectAdmission{
		OwnerContact: owner, Version: 2, Mode: binding.Mode, Product: binding.Product, PayerID: binding.PayerID,
		ActorID: binding.ActorID, SpaceID: binding.SpaceID, ProjectID: projectID, CommandID: binding.CommandID,
		RequestDigest: binding.RequestDigest, LimitsVersion: access.GrantVersion,
		ProfileVersion: businessProjectAdmissionProfileVersion, SubscriptionID: access.OwnerSubscriptionID,
		OwnerGeneration: access.OwnerGeneration, CreatedAt: at, ServiceID: access.ServiceID, PlanID: access.PlanID,
		PaidServiceProofID: access.PaidServiceProofID, OwnerRevision: access.OwnerRevision,
		UnlimitedProjects: access.UnlimitedProjects, UnlimitedContacts: access.UnlimitedContacts,
	}
	if err := admission.Validate(); err != nil {
		return ErrSharedProjectConflict
	}
	return tx.Insert(ctx, r)
}

// verifyBusinessProjectAdmissionReplay validates immutable creation facts,
// while current authority is independently re-read by the caller. Owner
// subscription generation is provenance and does not pin future renewals.
func verifyBusinessProjectAdmissionReplay(ctx context.Context, tx dal.ReadwriteTransaction, binding SharedProjectCreateBinding, projectID string) error {
	r, admission := models4datatug.NewProjectAdmissionRecord(binding.SpaceID, projectID)
	if err := tx.Get(ctx, r); err != nil {
		return ErrSharedProjectConflict
	}
	if admission.Validate() != nil || admission.Version != 2 || admission.Mode != binding.Mode || admission.Product != binding.Product || admission.PayerID != binding.PayerID || admission.ActorID != binding.ActorID || admission.SpaceID != binding.SpaceID || admission.ProjectID != projectID || admission.CommandID != binding.CommandID || admission.RequestDigest != binding.RequestDigest {
		return ErrSharedProjectConflict
	}
	return nil
}
