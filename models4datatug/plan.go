package models4datatug

import (
	"time"

	"github.com/dal-go/record"
)

const (
	SpacesCollection  = "spaces"
	PlanCollection    = "plan"
	AIUsageCollection = "aiUsage"
)

// NewCurrentPlanKey names only the live entitlement document. Test-mode
// reconciliation deliberately uses a different key and never feeds readers.
func NewCurrentPlanKey(accountID string) *record.Key {
	return record.NewKeyWithParentAndID(planCollectionKey(accountID), PlanCollection, "current")
}

func NewTestPlanKey(accountID string) *record.Key {
	return record.NewKeyWithParentAndID(planCollectionKey(accountID), PlanCollection, "test")
}

func NewAIUsageKey(accountID, periodID string) *record.Key {
	return record.NewKeyWithParentAndID(planCollectionKey(accountID), AIUsageCollection, periodID)
}

func planCollectionKey(accountID string) *record.Key {
	space := record.NewKeyWithID(SpacesCollection, accountID)
	return record.NewKeyWithParentAndID(space, UserExtCollection, "datatug")
}

// PlanLimits is the version-1 allowance carried by a paid plan or a response.
// A Free allowance comes from host configuration, never from a stored plan.
type PlanLimits struct {
	Contributors        int64    `json:"contributors" firestore:"contributors"`
	ProjectGuests       int64    `json:"projectGuests" firestore:"projectGuests"`
	ProjectContributors *int64   `json:"projectContributors,omitempty" firestore:"projectContributors,omitempty"`
	AIQuestions         int64    `json:"aiQuestions" firestore:"aiQuestions"`
	AIModelClasses      []string `json:"aiModelClasses" firestore:"aiModelClasses"`
	AIPaysFor           string   `json:"aiPaysFor" firestore:"aiPaysFor"`
}

// PlanRecord is the version-1 plan/current document. Test-mode reconciliation
// writes plan/test instead; readers of live entitlements never use that path.
type PlanRecord struct {
	V                int         `json:"v" firestore:"v"`
	Plan             string      `json:"plan" firestore:"plan"`
	Status           string      `json:"status" firestore:"status"`
	Period           string      `json:"period" firestore:"period"`
	PaidUntil        *time.Time  `json:"paidUntil,omitempty" firestore:"paidUntil,omitempty"`
	EndsAt           *time.Time  `json:"endsAt,omitempty" firestore:"endsAt,omitempty"`
	EndedReason      string      `json:"endedReason,omitempty" firestore:"endedReason,omitempty"`
	Founding         bool        `json:"founding" firestore:"founding"`
	Limits           *PlanLimits `json:"limits,omitempty" firestore:"limits,omitempty"`
	AIExtraQuestions int64       `json:"aiExtraQuestions,omitempty" firestore:"aiExtraQuestions,omitempty"`
	UpdatedAt        *time.Time  `json:"updatedAt,omitempty" firestore:"updatedAt,omitempty"`
}

// AIUsageRecord is the version-1 UTC-month usage document.
type AIUsageRecord struct {
	V         int              `json:"v" firestore:"v"`
	PeriodID  string           `json:"periodId" firestore:"periodId"`
	Used      int64            `json:"used" firestore:"used"`
	Questions int64            `json:"questions" firestore:"questions"`
	ByMember  map[string]int64 `json:"byMember,omitempty" firestore:"byMember,omitempty"`
	Capped    bool             `json:"capped,omitempty" firestore:"capped,omitempty"`
	ResetsAt  *time.Time       `json:"resetsAt,omitempty" firestore:"resetsAt,omitempty"`
	UpdatedAt *time.Time       `json:"updatedAt,omitempty" firestore:"updatedAt,omitempty"`
}
