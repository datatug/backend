package facade4datatug

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/datatug/backend/models4datatug"
)

var (
	ErrPersonalAccountUnavailable = errors.New("personal account unavailable")
	ErrForeignAccount             = errors.New("account is not the caller's personal account")
	ErrPlanUnavailable            = errors.New("plan response unavailable")
)

// PersonalAccount is an ownership assertion made by the host directory.
type PersonalAccount struct{ ID, Title string }

// PersonalAccountDirectory must prove ownership and provision the personal
// account through the host. This domain never infers an account from a user ID.
type PersonalAccountDirectory interface {
	PersonalAccount(context.Context, string) (PersonalAccount, error)
}

// PlanReader reads only plan/current; an unreadable record falls toward Free.
type PlanReader interface {
	ReadCurrentPlan(context.Context, string) (*models4datatug.PlanRecord, error)
}

// UsageReader reads the requested UTC month; nil means no count document.
type UsageReader interface {
	ReadUsage(context.Context, string, string) (*models4datatug.AIUsageRecord, error)
}

// AdmissionState supplies guards that are authoritative outside this module.
// Blocked is empty, daily, free_budget, or unverified.
type AdmissionState struct {
	TodayUsed int64
	Blocked   string
}
type AdmissionReader interface {
	ReadAdmission(context.Context, string, time.Time) (AdmissionState, error)
}

type PlanModel struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	Weight  int64  `json:"weight"`
	Default bool   `json:"default"`
}

// PlanConfig is supplied by the host, rather than encoded in plan policy. The
// two Free grants differ by the month of the first admitted question, which is
// independently read from the meter's server-only marker.
type PlanConfig struct {
	FreeFirstMonthLimits models4datatug.PlanLimits
	FreeLaterMonthLimits models4datatug.PlanLimits
	ProLimits            models4datatug.PlanLimits
	FreeModels           []PlanModel
	ProModels            []PlanModel
	DailyLimit           int64
	ActiveGrace          time.Duration
	PastDueGrace         time.Duration
	Enforced             bool
	UpgradeURL           string
	ManageURL            string
	SupportEmail         string
}

// PlanConfigReader receives the proved account and the one UTC instant sampled
// for this response; settings can change at runtime without implicit context.
type PlanConfigReader interface {
	ReadPlanConfig(context.Context, string, time.Time) (PlanConfig, error)
}
type PlanClock interface{ Now() time.Time }

// FirstAdmittedMonthReader reads the same immutable, server-only first-admission
// marker used by the AI meter. Empty means no question has been admitted yet.
// It is keyed by product and caller, with the proved personal account included
// so the host can reject a mismatched binding.
type FirstAdmittedMonthReader interface {
	ReadFirstAdmittedMonth(context.Context, string, string, string) (string, error)
}

// PersonalPayer is shared by plan reads and the later AI adapter. A foreign
// account hint is refused before any plan or usage read for that hint.
func ResolvePersonalPayer(ctx context.Context, callerID, accountHint string, directory PersonalAccountDirectory) (PersonalAccount, error) {
	if callerID == "" || directory == nil {
		return PersonalAccount{}, ErrPersonalAccountUnavailable
	}
	account, err := directory.PersonalAccount(ctx, callerID)
	if err != nil || account.ID == "" || account.Title == "" {
		return PersonalAccount{}, ErrPersonalAccountUnavailable
	}
	if accountHint != "" && accountHint != account.ID {
		return PersonalAccount{}, ErrForeignAccount
	}
	return account, nil
}

type PlanAccount struct {
	AccountID string `json:"accountId"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Plan      string `json:"plan"`
	Role      string `json:"role"`
	Pays      bool   `json:"pays"`
}
type PlanToday struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}
type PlanAI struct {
	Unit     string      `json:"unit"`
	Enforced bool        `json:"enforced"`
	PeriodID string      `json:"periodId"`
	Used     int64       `json:"used"`
	Limit    int64       `json:"limit"`
	Left     int64       `json:"left"`
	ResetsAt time.Time   `json:"resetsAt"`
	Today    PlanToday   `json:"today"`
	Blocked  *string     `json:"blocked"`
	Models   []PlanModel `json:"models"`
}
type PlanResponse struct {
	V             int                       `json:"v"`
	Payer         string                    `json:"payer"`
	AccountID     string                    `json:"accountId"`
	AccountTitle  string                    `json:"accountTitle"`
	Role          string                    `json:"role"`
	Plan          string                    `json:"plan"`
	EffectivePlan string                    `json:"effectivePlan"`
	Status        string                    `json:"status"`
	Period        string                    `json:"period"`
	PaidUntil     *time.Time                `json:"paidUntil,omitempty"`
	Founding      bool                      `json:"founding"`
	Limits        models4datatug.PlanLimits `json:"limits"`
	AI            PlanAI                    `json:"ai"`
	OwnKeyWorks   bool                      `json:"ownKeyWorks"`
	CanUpgrade    bool                      `json:"canUpgrade"`
	UpgradeURL    string                    `json:"upgradeUrl"`
	ManageURL     string                    `json:"manageUrl,omitempty"`
	SupportEmail  string                    `json:"supportEmail,omitempty"`
	Accounts      []PlanAccount             `json:"accounts"`
}

// PersonalPlanService composes ports without depending on transport or store.
type PersonalPlanService struct {
	Directory  PersonalAccountDirectory
	Plans      PlanReader
	Usage      UsageReader
	Admission  AdmissionReader
	Config     PlanConfigReader
	Clock      PlanClock
	FirstMonth FirstAdmittedMonthReader
}

func (s PersonalPlanService) Read(ctx context.Context, callerID, accountHint, _ string) (PlanResponse, error) {
	if s.Directory == nil || s.Plans == nil || s.Usage == nil || s.Admission == nil || s.Config == nil || s.Clock == nil || s.FirstMonth == nil {
		return PlanResponse{}, ErrPlanUnavailable
	}
	account, err := ResolvePersonalPayer(ctx, callerID, accountHint, s.Directory)
	if err != nil {
		return PlanResponse{}, err
	}
	now := s.Clock.Now().UTC()
	config, err := s.Config.ReadPlanConfig(ctx, account.ID, now)
	if err != nil || validateConfig(config) != nil {
		return PlanResponse{}, ErrPlanUnavailable
	}
	periodID := now.Format("2006-01")
	reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	record, planReadErr := s.Plans.ReadCurrentPlan(ctx, account.ID)
	if planReadErr != nil {
		record = nil
	} // discard even a partial value on error
	plan, effective, status, billingPeriod := "free", "free", "none", "none"
	var paidUntil *time.Time
	founding := false
	limits := config.FreeFirstMonthLimits
	models := config.FreeModels
	var extra int64
	if record != nil && record.V == 1 {
		if record.Plan == "pro" || record.Plan == "free" {
			plan = record.Plan
			status = record.Status
			billingPeriod = record.Period
			paidUntil = record.PaidUntil
			founding = record.Founding
		}
		if plan == "pro" && record.Limits != nil && paidAccessHolds(record, now, config) {
			candidate := *record.Limits
			if candidate.ProjectContributors == nil {
				candidate.ProjectContributors = config.ProLimits.ProjectContributors
			}
			if record.AIExtraQuestions >= 0 && validateLimits(candidate) == nil &&
				validateModels(config.ProModels, candidate) == nil &&
				candidate.AIQuestions <= math.MaxInt64-record.AIExtraQuestions {
				effective = "pro"
				limits = candidate
				models = config.ProModels
				extra = record.AIExtraQuestions
			}
		}
	}
	if effective == "free" {
		firstMonth, err := s.FirstMonth.ReadFirstAdmittedMonth(ctx, "datatug", callerID, account.ID)
		if err != nil || !validFirstAdmittedMonth(firstMonth, periodID) {
			return PlanResponse{}, ErrPlanUnavailable
		}
		if firstMonth != "" && firstMonth != periodID {
			limits = config.FreeLaterMonthLimits
		}
	}
	limit := limits.AIQuestions + extra
	usage, err := s.Usage.ReadUsage(ctx, account.ID, periodID)
	if err != nil {
		return PlanResponse{}, ErrPlanUnavailable
	}
	used, capped := int64(0), false
	if usage != nil {
		if usage.V != 1 || usage.PeriodID != periodID || usage.Used < 0 || usage.Questions < 0 {
			return PlanResponse{}, ErrPlanUnavailable
		}
		used, capped = usage.Used, usage.Capped
	}
	admission, err := s.Admission.ReadAdmission(ctx, account.ID, now)
	if err != nil || admission.TodayUsed < 0 || !validBlocked(admission.Blocked) {
		return PlanResponse{}, ErrPlanUnavailable
	}
	left := int64(0)
	if !capped && used < limit {
		left = limit - used
	}
	var blocked *string
	if capped {
		reason := "monthly"
		blocked = &reason
	} else if config.Enforced && (admission.TodayUsed >= config.DailyLimit || admission.Blocked == "daily") {
		reason := "daily"
		blocked = &reason
	} else if admission.Blocked == "free_budget" || admission.Blocked == "unverified" {
		blocked = &admission.Blocked
	}
	response := PlanResponse{
		V: 1, Payer: "personal", AccountID: account.ID, AccountTitle: account.Title, Role: "owner",
		Plan: plan, EffectivePlan: effective, Status: status, Period: billingPeriod, PaidUntil: paidUntil,
		Founding: founding, Limits: limits, AI: PlanAI{Unit: "questions", Enforced: config.Enforced,
			PeriodID: periodID, Used: used, Limit: limit, Left: left, ResetsAt: reset,
			Today: PlanToday{Used: admission.TodayUsed, Limit: config.DailyLimit}, Blocked: blocked, Models: models},
		OwnKeyWorks: true, CanUpgrade: true, UpgradeURL: config.UpgradeURL,
		SupportEmail: config.SupportEmail,
		Accounts:     []PlanAccount{{AccountID: account.ID, Kind: "personal", Title: account.Title, Plan: effective, Role: "owner", Pays: true}},
	}
	if status == "active" || status == "trialing" || status == "past_due" {
		response.ManageURL = config.ManageURL
	}
	return response, nil
}

func paidAccessHolds(record *models4datatug.PlanRecord, now time.Time, config PlanConfig) bool {
	if record.PaidUntil == nil {
		return false
	}
	var grace time.Duration
	switch record.Status {
	case "active", "trialing":
		grace = config.ActiveGrace
	case "past_due":
		grace = config.PastDueGrace
	default:
		return false
	}
	if record.EndsAt != nil && !now.Before(*record.EndsAt) {
		return false
	}
	return !now.After(record.PaidUntil.Add(grace))
}

func validateLimits(l models4datatug.PlanLimits) error {
	if l.Contributors < 1 || l.ProjectGuests < 0 || l.AIQuestions < 0 || l.AIPaysFor != "owner" || len(l.AIModelClasses) == 0 {
		return ErrPlanUnavailable
	}
	if l.ProjectContributors != nil && *l.ProjectContributors < 1 {
		return ErrPlanUnavailable
	}
	return nil
}
func validateModels(models []PlanModel, limits models4datatug.PlanLimits) error {
	if len(models) == 0 {
		return ErrPlanUnavailable
	}
	defaultCount, minWeight, defaultWeight := 0, int64(math.MaxInt64), int64(0)
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		allowed := false
		for _, class := range limits.AIModelClasses {
			if class == model.Class {
				allowed = true
				break
			}
		}
		if model.ID == "" || !allowed || model.Weight <= 0 || seen[model.ID] {
			return ErrPlanUnavailable
		}
		seen[model.ID] = true
		if model.Weight < minWeight {
			minWeight = model.Weight
		}
		if model.Default {
			defaultCount++
			defaultWeight = model.Weight
		}
	}
	if defaultCount != 1 || defaultWeight != minWeight {
		return ErrPlanUnavailable
	}
	return nil
}
func validateConfig(config PlanConfig) error {
	if config.DailyLimit <= 0 || config.ActiveGrace < 0 || config.PastDueGrace <= config.ActiveGrace || config.UpgradeURL == "" {
		return ErrPlanUnavailable
	}
	if err := validateLimits(config.FreeFirstMonthLimits); err != nil {
		return err
	}
	if err := validateLimits(config.FreeLaterMonthLimits); err != nil {
		return err
	}
	if err := validateLimits(config.ProLimits); err != nil {
		return err
	}
	if config.ProLimits.ProjectContributors == nil {
		return ErrPlanUnavailable
	}
	if err := validateModels(config.FreeModels, config.FreeFirstMonthLimits); err != nil {
		return err
	}
	if err := validateModels(config.FreeModels, config.FreeLaterMonthLimits); err != nil {
		return err
	}
	if err := validateModels(config.ProModels, config.ProLimits); err != nil {
		return err
	}
	return nil
}

func validFirstAdmittedMonth(firstMonth, periodID string) bool {
	if firstMonth == "" {
		return true
	}
	month, err := time.Parse("2006-01", firstMonth)
	return err == nil && month.Format("2006-01") == firstMonth && firstMonth <= periodID
}
func validBlocked(reason string) bool {
	switch reason {
	case "", "daily", "free_budget", "unverified":
		return true
	default:
		return false
	}
}
