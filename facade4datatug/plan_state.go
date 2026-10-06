package facade4datatug

import (
	"time"

	"github.com/datatug/backend/models4datatug"
)

// PlanFacts are plain, provider-independent inputs to the plan-state mapping.
type PlanFacts struct {
	AccountKind               string                     `json:"accountKind"`
	Tier                      string                     `json:"tier"`
	Period                    string                     `json:"period"`
	ProviderStatus            string                     `json:"providerStatus"`
	PaidUntil                 *time.Time                 `json:"paidUntil,omitempty"`
	EndsAt                    *time.Time                 `json:"endsAt,omitempty"`
	LastInvoiceRefundedInFull bool                       `json:"lastInvoiceRefundedInFull"`
	TerminalFullRefund        bool                       `json:"terminalFullRefund,omitempty"`
	RefundedAt                *time.Time                 `json:"refundedAt,omitempty"`
	FirstPaidAt               *time.Time                 `json:"firstPaidAt,omitempty"`
	Founding                  bool                       `json:"founding"`
	Grants                    *models4datatug.PlanLimits `json:"grants,omitempty"`
}

// PlanStateResult names a write, leave, or refusal. The writer retains the
// previous aiExtraQuestions and stamps updatedAt outside this pure function.
type PlanStateResult struct {
	Outcome string                     `json:"outcome"`
	Reason  string                     `json:"reason,omitempty"`
	Record  *models4datatug.PlanRecord `json:"record,omitempty"`
}

// PlanStateFor follows the ordered precedence of the version-1 state table.
// This slice grants paid access only to personal Pro; ended states remain
// mappable even when a provider reports a now-unsupported tier.
func PlanStateFor(f PlanFacts) PlanStateResult {
	if f.AccountKind == "personal" && f.Tier == "pro" && f.TerminalFullRefund {
		switch f.ProviderStatus {
		case "active", "trialing", "past_due", "unpaid", "paused", "canceled":
			return PlanStateResult{Outcome: "write", Record: &models4datatug.PlanRecord{
				V: 1, Plan: "free", Status: "ended", Period: "none", PaidUntil: f.PaidUntil,
				EndedReason: "refunded", Founding: false,
			}}
		}
	}
	ended := ""
	switch f.ProviderStatus {
	case "unpaid":
		ended = "unpaid"
	case "paused":
		ended = "paused"
	case "canceled":
		ended = "canceled"
		if f.LastInvoiceRefundedInFull {
			ended = "refunded"
		}
	}
	if ended != "" {
		return PlanStateResult{Outcome: "write", Record: &models4datatug.PlanRecord{
			V: 1, Plan: "free", Status: "ended", Period: "none", PaidUntil: f.PaidUntil,
			EndedReason: ended, Founding: false,
		}}
	}
	if f.ProviderStatus == "incomplete" || f.ProviderStatus == "incomplete_expired" {
		return PlanStateResult{Outcome: "leave"}
	}
	if f.Tier == "" {
		return PlanStateResult{Outcome: "refuse", Reason: "unknown_tier"}
	}
	if f.Tier != "pro" && f.Tier != "team" && f.Tier != "business" && f.Tier != "company" {
		return PlanStateResult{Outcome: "refuse", Reason: "unknown_tier"}
	}
	if f.AccountKind != "personal" || f.Tier != "pro" {
		return PlanStateResult{Outcome: "refuse", Reason: "tier_not_for_account"}
	}
	if f.ProviderStatus == "active" || f.ProviderStatus == "trialing" || f.ProviderStatus == "past_due" {
		if f.LastInvoiceRefundedInFull {
			return PlanStateResult{Outcome: "write", Record: &models4datatug.PlanRecord{
				V: 1, Plan: "free", Status: "ended", Period: "none", PaidUntil: f.PaidUntil,
				EndedReason: "refunded", Founding: false,
			}}
		}
		return PlanStateResult{Outcome: "write", Record: &models4datatug.PlanRecord{
			V: 1, Plan: "pro", Status: f.ProviderStatus, Period: f.Period,
			PaidUntil: f.PaidUntil, EndsAt: f.EndsAt, Founding: f.Founding, Limits: f.Grants,
		}}
	}
	return PlanStateResult{Outcome: "refuse", Reason: "unknown_status"}
}
