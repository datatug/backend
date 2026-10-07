// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
)

func readLinkedProjectAdmission(ctx context.Context, tx dal.ReadTransaction, ref contract4linkage.RelationshipEntityRef, project *models4datatug.SharedLinkedProject) (*models4datatug.ProjectAdmission, error) {
	r, admission := models4datatug.NewProjectAdmissionRecord(string(ref.SpaceID), ref.ItemRef.ItemID)
	if err := tx.Get(ctx, r); err != nil {
		return nil, err
	}
	if admission.Validate() != nil || admission.Mode != "live" || admission.Product != "datatug" || admission.SpaceID != string(ref.SpaceID) || admission.ProjectID != ref.ItemRef.ItemID || admission.OwnerContact.Validate() != nil || project.Created == nil || !project.Created.At.Equal(admission.CreatedAt) {
		return nil, ErrSharedProjectConflict
	}
	rr, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(admission.SpaceID, admission.CommandID)
	if err := tx.Get(ctx, rr); err != nil {
		return nil, err
	}
	if receipt.Validate() != nil || receipt.ActorID != admission.ActorID || receipt.PayerID != admission.PayerID || receipt.Mode != admission.Mode || receipt.Product != admission.Product || receipt.SpaceID != admission.SpaceID || receipt.ProjectID != admission.ProjectID || receipt.CommandID != admission.CommandID || receipt.RequestDigest != admission.RequestDigest || receipt.OwnerContact != admission.OwnerContact || !receipt.CreatedAt.Equal(admission.CreatedAt) {
		return nil, ErrSharedProjectConflict
	}
	return admission, nil
}
