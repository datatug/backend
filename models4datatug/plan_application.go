package models4datatug

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/dal-go/record"
)

const (
	PlanApplicationCollection = "datatugPlanApplications"
	PaidMoneyMonthCollection  = "datatugPaidMoneyMonths"
)

// privatePlanID is collision-safe across modes, families, accounts and months.
// Neither collection is under a member-readable /spaces subtree.
func privatePlanID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		h.Write(length[:])
		h.Write([]byte(part))
	}
	return "v1-" + hex.EncodeToString(h.Sum(nil))
}

func NewPlanApplicationKey(mode, family, accountID string) *record.Key {
	return record.NewKeyWithID(PlanApplicationCollection, privatePlanID(mode, family, accountID))
}

func NewPaidMoneyMonthKey(mode, family, accountID, periodID string) *record.Key {
	return record.NewKeyWithID(PaidMoneyMonthCollection, privatePlanID(mode, family, accountID, periodID))
}

// PlanApplication is private replay and last-actual-plan-write provenance.
// Latest applied revision may be basis-only, unlike the last Pro plan write.
type PlanApplication struct {
	V                             int      `json:"v" firestore:"v"`
	Mode                          string   `json:"mode" firestore:"mode"`
	Family                        string   `json:"family" firestore:"family"`
	AccountID                     string   `json:"accountId" firestore:"accountId"`
	OwnerSubscriptionID           string   `json:"ownerSubscriptionId" firestore:"ownerSubscriptionId"`
	OwnerGeneration               int64    `json:"ownerGeneration" firestore:"ownerGeneration"`
	SubscriptionRevision          int64    `json:"subscriptionRevision" firestore:"subscriptionRevision"`
	EffectDigest                  string   `json:"effectDigest" firestore:"effectDigest"`
	BasisVersion                  int64    `json:"basisVersion" firestore:"basisVersion"`
	BasisPeriods                  []string `json:"basisPeriods" firestore:"basisPeriods"`
	LimitsVersion                 string   `json:"limitsVersion,omitempty" firestore:"limitsVersion,omitempty"`
	LastFullEffectSubscriptionID  string   `json:"lastFullEffectSubscriptionId,omitempty" firestore:"lastFullEffectSubscriptionId,omitempty"`
	LastFullEffectOwnerGeneration int64    `json:"lastFullEffectOwnerGeneration,omitempty" firestore:"lastFullEffectOwnerGeneration,omitempty"`
	LastFullEffectRevision        int64    `json:"lastFullEffectRevision,omitempty" firestore:"lastFullEffectRevision,omitempty"`
	LastFullEffectQuoteKey        string   `json:"lastFullEffectQuoteKey,omitempty" firestore:"lastFullEffectQuoteKey,omitempty"`
	LastProSubscriptionID         string   `json:"lastProSubscriptionId,omitempty" firestore:"lastProSubscriptionId,omitempty"`
	LastProOwnerGeneration        int64    `json:"lastProOwnerGeneration,omitempty" firestore:"lastProOwnerGeneration,omitempty"`
	LastProQuoteKey               string   `json:"lastProQuoteKey,omitempty" firestore:"lastProQuoteKey,omitempty"`
	LastProPlanID                 string   `json:"lastProPlanId,omitempty" firestore:"lastProPlanId,omitempty"`
	LastProPaidServiceProofID     string   `json:"lastProPaidServiceProofId,omitempty" firestore:"lastProPaidServiceProofId,omitempty"`
}

// PaidMoneyMonth matches the core paid-month fields. Core's published
// allocator/admission release must verify this shape before host binding.
type PaidMoneyMonth struct {
	V                   int    `json:"v" firestore:"v"`
	Mode                string `json:"mode" firestore:"mode"`
	AccountID           string `json:"accountId" firestore:"accountId"`
	PeriodID            string `json:"periodId" firestore:"periodId"`
	BasisRevision       int64  `json:"basisRevision" firestore:"basisRevision"`
	BasisMicroEUR       int64  `json:"basisMicroEUR" firestore:"basisMicroEUR"`
	SettledMicroEUR     int64  `json:"settledMicroEUR" firestore:"settledMicroEUR"`
	OutstandingMicroEUR int64  `json:"outstandingMicroEUR" firestore:"outstandingMicroEUR"`
	Frozen              bool   `json:"frozen" firestore:"frozen"`
}
