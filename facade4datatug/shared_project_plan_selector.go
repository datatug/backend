package facade4datatug

import (
	"context"

	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

// resolveSharedProjectCreateBinding uses an explicit plan intent when a
// service supports both plans. An omitted intent preserves Pro as the default
// for the unified service; single-plan services keep their configured plan.
// The intent never grants access and cannot supply a payer.
func (s *SharedProjectService) resolveSharedProjectCreateBinding(ctx context.Context, actorID, spaceID, commandID, title string, intent SharedProjectBillingIntent) (SharedProjectCreateBinding, error) {
	binding := SharedProjectCreateBinding{ActorID: actorID, SpaceID: spaceID, CommandID: commandID,
		RequestDigest: models4datatug.SharedProjectCreateDigest(actorID, spaceID, commandID, title)}
	if s == nil || s.paid == nil && s.business == nil {
		if intent != "" {
			return SharedProjectCreateBinding{}, ErrSharedProjectUnauthorized
		}
		return binding, nil
	}
	if intent == "" {
		if s != nil && s.paid != nil {
			intent = BillingIntentPersonalPro
		} else if s != nil && s.business != nil {
			intent = BillingIntentSpaceBusiness
		}
	}
	switch intent {
	case BillingIntentPersonalPro:
		if s == nil || s.paid == nil {
			return SharedProjectCreateBinding{}, ErrSharedProjectUnauthorized
		}
		account, err := ResolvePersonalPayer(ctx, actorID, "", s.paid.Directory)
		if err != nil || models4datatug.ValidateSharedProjectIdentifier(account.ID) != nil {
			return SharedProjectCreateBinding{}, ErrSharedProjectUnauthorized
		}
		binding.PayerID, binding.Mode, binding.Product = account.ID, s.paid.Mode, s.paid.Product
	case BillingIntentSpaceBusiness:
		if s == nil || s.business == nil {
			return SharedProjectCreateBinding{}, ErrSharedProjectUnauthorized
		}
		binding.PayerID, binding.Mode, binding.Product = spaceID, string(s.business.Mode()), BusinessProjectProductID
	default:
		return SharedProjectCreateBinding{}, ErrSharedProjectInvalid
	}
	binding.RequestDigest = models4datatug.PaidSharedProjectCreateDigest(actorID, spaceID, commandID, title, binding.PayerID, binding.Mode, binding.Product)
	return binding, nil
}

func isBusinessProjectBinding(binding SharedProjectCreateBinding) bool {
	return (binding.Mode == string(contract4paymentus.ModeTest) || binding.Mode == string(contract4paymentus.ModeLive)) &&
		binding.Product == BusinessProjectProductID && binding.PayerID == binding.SpaceID
}
