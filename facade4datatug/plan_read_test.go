package facade4datatug

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/datatug/backend/models4datatug"
)

type testDirectory struct {
	account PersonalAccount
	err     error
	calls   int
}

func (d *testDirectory) PersonalAccount(_ context.Context, caller string) (PersonalAccount, error) {
	d.calls++
	if caller != "caller" {
		return PersonalAccount{}, errors.New("unknown caller")
	}
	return d.account, d.err
}

type testPlanReader struct {
	record *models4datatug.PlanRecord
	err    error
	ids    []string
}

func (p *testPlanReader) ReadCurrentPlan(_ context.Context, id string) (*models4datatug.PlanRecord, error) {
	p.ids = append(p.ids, id)
	return p.record, p.err
}

type testUsageReader struct {
	usage  *models4datatug.AIUsageRecord
	err    error
	ids    []string
	months []string
}

func (u *testUsageReader) ReadUsage(_ context.Context, id, month string) (*models4datatug.AIUsageRecord, error) {
	u.ids = append(u.ids, id)
	u.months = append(u.months, month)
	return u.usage, u.err
}

type testAdmissionReader struct {
	state    AdmissionState
	err      error
	instants []time.Time
}

type testCallerAdmissionReader struct {
	state               AdmissionState
	err                 error
	callerID, accountID string
	instant             time.Time
	calls               int
}

func (a *testCallerAdmissionReader) ReadAdmissionForCaller(_ context.Context, callerID, accountID string, now time.Time) (AdmissionState, error) {
	a.calls++
	a.callerID, a.accountID, a.instant = callerID, accountID, now
	return a.state, a.err
}

func (a *testAdmissionReader) ReadAdmission(_ context.Context, _ string, now time.Time) (AdmissionState, error) {
	a.instants = append(a.instants, now)
	return a.state, a.err
}

type testConfigReader struct {
	config  PlanConfig
	err     error
	observe func(string, time.Time)
}

func (c testConfigReader) ReadPlanConfig(_ context.Context, accountID string, now time.Time) (PlanConfig, error) {
	if c.observe != nil {
		c.observe(accountID, now)
	}
	return c.config, c.err
}

type testFirstMonthReader struct {
	month                    string
	err                      error
	calls                    int
	product, caller, account string
}

func (f *testFirstMonthReader) ReadFirstAdmittedMonth(_ context.Context, product, caller, account string) (string, error) {
	f.calls++
	f.product, f.caller, f.account = product, caller, account
	return f.month, f.err
}

type testClock struct {
	now   time.Time
	calls int
}

func (c *testClock) Now() time.Time { c.calls++; return c.now }

func validPlanTestService() (PersonalPlanService, *testDirectory, *testPlanReader, *testUsageReader, *testAdmissionReader, *testClock) {
	five := int64(5)
	free := models4datatug.PlanLimits{Contributors: 1, ProjectGuests: 2, AIQuestions: 7, AIModelClasses: []string{"fast"}, AIPaysFor: "owner"}
	pro := models4datatug.PlanLimits{Contributors: 1, ProjectGuests: 3, ProjectContributors: &five, AIQuestions: 11, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner"}
	d := &testDirectory{account: PersonalAccount{ID: "personal-1", Title: "Example"}}
	p := &testPlanReader{}
	u := &testUsageReader{}
	a := &testAdmissionReader{}
	c := &testClock{now: time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)}
	later := free
	later.AIQuestions = 4
	config := PlanConfig{FreeFirstMonthLimits: free, FreeLaterMonthLimits: later, ProLimits: pro, FreeModels: []PlanModel{{ID: "fast", Class: "fast", Weight: 1, Default: true}}, ProModels: []PlanModel{{ID: "fast", Class: "fast", Weight: 1, Default: true}, {ID: "standard", Class: "standard", Weight: 3}}, DailyLimit: 17, ActiveGrace: time.Hour, PastDueGrace: 72 * time.Hour, Enforced: true, UpgradeURL: "https://example.com/pro", ManageURL: "https://example.com/manage"}
	return PersonalPlanService{Directory: d, Plans: p, Usage: u, Admission: a, Config: testConfigReader{config: config}, Clock: c, FirstMonth: &testFirstMonthReader{}}, d, p, u, a, c
}

func TestPersonalPlanFreeProEndedAndRollover(t *testing.T) {
	s, d, p, u, _, clock := validPlanTestService()
	u.usage = &models4datatug.AIUsageRecord{V: 1, PeriodID: "2026-03", Used: 3, Questions: 3}
	read := func() PlanResponse {
		t.Helper()
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	free := read()
	if free.Plan != "free" || free.AI.Left != 4 || len(free.Accounts) != 1 || !free.Accounts[0].Pays || free.Payer != "personal" {
		t.Fatal(free)
	}
	five := int64(5)
	facts := PlanFacts{AccountKind: "personal", Tier: "pro", Period: "month", ProviderStatus: "active", PaidUntil: timePtr(time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)), Grants: &models4datatug.PlanLimits{Contributors: 1, ProjectGuests: 3, ProjectContributors: &five, AIQuestions: 11, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner"}}
	result := PlanStateFor(facts)
	if result.Outcome != "write" {
		t.Fatal(result)
	}
	p.record = result.Record
	pro := read()
	if pro.Plan != "pro" || pro.EffectivePlan != "pro" || pro.Limits.ProjectContributors == nil || *pro.Limits.ProjectContributors != 5 || pro.AI.Left != 8 || pro.ManageURL == "" {
		t.Fatal(pro)
	}
	if len(d.account.ID) == 0 || p.ids[0] != "personal-1" || u.ids[1] != "personal-1" {
		t.Fatal(p.ids, u.ids)
	}
	clock.now = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	u.usage = nil
	stale := read()
	if stale.EffectivePlan != "free" || stale.Plan != "pro" || stale.AI.Used != 0 || stale.AI.PeriodID != "2026-04" || !stale.AI.ResetsAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(stale)
	}
	ended := facts
	ended.ProviderStatus = "canceled"
	p.record = PlanStateFor(ended).Record
	last := read()
	if last.Plan != "free" || last.Status != "ended" || last.Founding || last.ManageURL != "" || last.Limits.ProjectContributors != nil {
		t.Fatal(last)
	}
	if clock.calls != 4 || len(u.months) != 4 || u.months[2] != "2026-04" {
		t.Fatal(clock.calls, u.months)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestPersonalPlanRejectsForeignBeforeReads(t *testing.T) {
	s, _, p, u, _, _ := validPlanTestService()
	_, err := s.Read(context.Background(), "caller", "another-account", "project")
	if !errors.Is(err, ErrForeignAccount) || len(p.ids) != 0 || len(u.ids) != 0 {
		t.Fatal(err, p.ids, u.ids)
	}
}

func TestPersonalPlanFailuresAndBoundaries(t *testing.T) {
	t.Run("unavailable personal account", func(t *testing.T) {
		s, d, p, _, _, _ := validPlanTestService()
		d.account = PersonalAccount{}
		_, err := s.Read(context.Background(), "caller", "", "")
		if !errors.Is(err, ErrPersonalAccountUnavailable) || len(p.ids) != 0 {
			t.Fatal(err, p.ids)
		}
	})
	t.Run("unreadable plan grants free", func(t *testing.T) {
		s, _, p, _, _, _ := validPlanTestService()
		pro := s.Config.(testConfigReader).config.ProLimits
		paid := time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC)
		p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: &paid, Limits: &pro}
		p.err = errors.New("read")
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil || got.EffectivePlan != "free" {
			t.Fatal(got, err)
		}
	})
	t.Run("usage outage", func(t *testing.T) {
		s, _, _, u, _, _ := validPlanTestService()
		u.err = errors.New("read")
		_, err := s.Read(context.Background(), "caller", "", "")
		if !errors.Is(err, ErrPlanUnavailable) {
			t.Fatal(err)
		}
	})
	t.Run("count and cap", func(t *testing.T) {
		s, _, _, u, a, _ := validPlanTestService()
		u.usage = &models4datatug.AIUsageRecord{V: 1, PeriodID: "2026-03", Used: 7, Capped: true}
		a.state.TodayUsed = 17
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil || got.AI.Left != 0 || got.AI.Blocked == nil || *got.AI.Blocked != "monthly" {
			t.Fatal(got, err)
		}
	})
	t.Run("monthly exhausted is not blocked", func(t *testing.T) {
		s, _, _, u, _, _ := validPlanTestService()
		u.usage = &models4datatug.AIUsageRecord{V: 1, PeriodID: "2026-03", Used: 7}
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil || got.AI.Left != 0 || got.AI.Blocked != nil {
			t.Fatal(got, err)
		}
	})
	t.Run("daily budget and unverified", func(t *testing.T) {
		for _, reason := range []string{"daily", "free_budget", "unverified"} {
			s, _, _, _, a, _ := validPlanTestService()
			a.state.Blocked = reason
			got, err := s.Read(context.Background(), "caller", "", "")
			if err != nil || got.AI.Blocked == nil || *got.AI.Blocked != reason {
				t.Fatal(reason, got, err)
			}
		}
	})
	t.Run("invalid stored overflow falls to free", func(t *testing.T) {
		s, _, p, _, _, _ := validPlanTestService()
		p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: timePtr(time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC)), Limits: &models4datatug.PlanLimits{Contributors: 1, AIQuestions: math.MaxInt64, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner"}, AIExtraQuestions: 1}
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil || got.EffectivePlan != "free" || got.AI.Limit != 7 {
			t.Fatal(got, err)
		}
	})
	t.Run("bad configuration", func(t *testing.T) {
		s, _, _, _, _, _ := validPlanTestService()
		s.Config = testConfigReader{config: PlanConfig{}}
		_, err := s.Read(context.Background(), "caller", "", "")
		if !errors.Is(err, ErrPlanUnavailable) {
			t.Fatal(err)
		}
	})
}

func TestPersonalPlanPaidAccessBoundaries(t *testing.T) {
	s, _, p, _, _, clock := validPlanTestService()
	pro := s.Config.(testConfigReader).config.ProLimits
	paid := clock.now
	p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: &paid, Limits: &pro}
	assertPlan := func(want string) {
		t.Helper()
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil || got.EffectivePlan != want {
			t.Fatalf("at %s status %s: %+v, %v", clock.now, p.record.Status, got, err)
		}
	}
	clock.now = paid.Add(time.Hour)
	assertPlan("pro") // later than, not at, paidUntil plus grace
	clock.now = clock.now.Add(time.Nanosecond)
	assertPlan("free")
	p.record.Status = "trialing"
	clock.now = paid.Add(30 * time.Minute)
	assertPlan("pro")
	p.record.Status = "past_due"
	clock.now = paid.Add(72 * time.Hour)
	assertPlan("pro")
	clock.now = clock.now.Add(time.Nanosecond)
	assertPlan("free")
	p.record.Status = "active"
	end := paid.Add(20 * time.Minute)
	p.record.EndsAt = &end
	clock.now = end.Add(-time.Nanosecond)
	assertPlan("pro")
	clock.now = end
	assertPlan("free") // scheduled cancellation is effective at endsAt
	p.record.EndsAt = nil
	p.record.PaidUntil = nil
	assertPlan("free")
	p.record.PaidUntil = &paid
	p.record.Status = "ended"
	assertPlan("free")
}

func TestPersonalPlanConfigAndInputsFailClosed(t *testing.T) {
	if _, err := ResolvePersonalPayer(context.Background(), "", "", nil); !errors.Is(err, ErrPersonalAccountUnavailable) {
		t.Fatal(err)
	}
	s, _, _, _, _, _ := validPlanTestService()
	s.Directory = nil
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	s, _, p, u, a, _ := validPlanTestService()
	base := s.Config.(testConfigReader).config
	badConfigs := []struct {
		name  string
		alter func(*PlanConfig)
	}{
		{"daily", func(c *PlanConfig) { c.DailyLimit = 0 }},
		{"free limits", func(c *PlanConfig) { c.FreeFirstMonthLimits.Contributors = 0 }},
		{"later free limits", func(c *PlanConfig) { c.FreeLaterMonthLimits.Contributors = 0 }},
		{"later free model class", func(c *PlanConfig) { c.FreeLaterMonthLimits.AIModelClasses = []string{"standard"} }},
		{"pro limits", func(c *PlanConfig) { c.ProLimits.AIQuestions = -1 }},
		{"pro project contributors", func(c *PlanConfig) { c.ProLimits.ProjectContributors = nil }},
		{"free models", func(c *PlanConfig) { c.FreeModels = nil }},
		{"pro models", func(c *PlanConfig) { c.ProModels[0].Default = false }},
	}
	for _, tc := range badConfigs {
		t.Run(tc.name, func(t *testing.T) {
			copy := base
			copy.FreeModels = append([]PlanModel(nil), base.FreeModels...)
			copy.ProModels = append([]PlanModel(nil), base.ProModels...)
			tc.alter(&copy)
			s.Config = testConfigReader{config: copy}
			if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
				t.Fatal(err)
			}
		})
	}
	s.Config = testConfigReader{config: base, err: errors.New("configuration down")}
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	s.Config = testConfigReader{config: base}
	badUsage := []models4datatug.AIUsageRecord{{V: 2, PeriodID: "2026-03"}, {V: 1, PeriodID: "other"}, {V: 1, PeriodID: "2026-03", Used: -1}, {V: 1, PeriodID: "2026-03", Questions: -1}}
	for _, usage := range badUsage {
		u.usage = &usage
		if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
			t.Fatal(usage, err)
		}
	}
	u.usage = nil
	a.err = errors.New("daily unavailable")
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	a.err = nil
	a.state.TodayUsed = -1
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	a.state.TodayUsed = 0
	a.state.Blocked = "unknown"
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	a.state.Blocked = ""
	p.record = &models4datatug.PlanRecord{V: 99, Plan: "pro"}
	if got, err := s.Read(context.Background(), "caller", "", ""); err != nil || got.EffectivePlan != "free" {
		t.Fatal(got, err)
	}
}

func TestPersonalPlanStoredGrantAndCatalogChanges(t *testing.T) {
	s, _, p, _, _, clock := validPlanTestService()
	config := s.Config.(testConfigReader).config
	pro := config.ProLimits
	pro.ProjectContributors = nil // older document gets current configured per-project grant
	p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: timePtr(clock.now.Add(time.Hour)), Limits: &pro, AIExtraQuestions: 2}
	got, err := s.Read(context.Background(), "caller", "", "")
	if err != nil || got.AI.Limit != 13 || got.Limits.ProjectContributors == nil || *got.Limits.ProjectContributors != 5 {
		t.Fatal(got, err)
	}
	config.ProLimits.ProjectContributors = timeInt64(6)
	s.Config = testConfigReader{config: config}
	got, err = s.Read(context.Background(), "caller", "", "")
	if err != nil || *got.Limits.ProjectContributors != 6 {
		t.Fatal(got, err)
	}
	p.record.Limits.ProjectContributors = timeInt64(5)
	got, err = s.Read(context.Background(), "caller", "", "")
	if err != nil || *got.Limits.ProjectContributors != 5 {
		t.Fatal(got, err)
	}
}
func timeInt64(value int64) *int64 { return &value }

func TestPlanValidationRejectsInvalidModelsAndLimits(t *testing.T) {
	limits := models4datatug.PlanLimits{Contributors: 1, AIQuestions: 1, AIModelClasses: []string{"fast"}, AIPaysFor: "owner"}
	for _, invalid := range []models4datatug.PlanLimits{{Contributors: 0}, {Contributors: 1, ProjectGuests: -1}, {Contributors: 1, AIQuestions: -1}, {Contributors: 1, AIQuestions: 1, AIPaysFor: "contributors", AIModelClasses: []string{"fast"}}, {Contributors: 1, AIQuestions: 1, AIPaysFor: "owner"}, {Contributors: 1, AIQuestions: 1, AIPaysFor: "owner", AIModelClasses: []string{"fast"}, ProjectContributors: timeInt64(0)}} {
		if validateLimits(invalid) == nil {
			t.Fatal(invalid)
		}
	}
	model := PlanModel{ID: "fast-1", Class: "fast", Weight: 1, Default: true}
	for _, invalid := range [][]PlanModel{nil, {{ID: "", Class: "fast", Weight: 1, Default: true}}, {{ID: "other", Class: "standard", Weight: 1, Default: true}}, {{ID: "fast-1", Class: "fast", Weight: 0, Default: true}}, {model, model}, {{ID: "fast-1", Class: "fast", Weight: 1}}, {model, {ID: "fast-2", Class: "fast", Weight: 1, Default: true}}, {{ID: "heavy", Class: "fast", Weight: 3, Default: true}, {ID: "light", Class: "fast", Weight: 1}}} {
		if validateModels(invalid, limits) == nil {
			t.Fatal(invalid)
		}
	}
	if validateModels([]PlanModel{model}, limits) != nil {
		t.Fatal("valid catalog refused")
	}
}

func TestPersonalPlanRejectsInvalidStoredGrantsAndAppliesDailyCeiling(t *testing.T) {
	s, _, p, _, admission, clock := validPlanTestService()
	pro := s.Config.(testConfigReader).config.ProLimits
	p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: timePtr(clock.now.Add(time.Hour)), Limits: &pro}
	read := func() PlanResponse {
		t.Helper()
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	p.record.Limits.Contributors = 0
	if got := read(); got.EffectivePlan != "free" {
		t.Fatal(got)
	}
	p.record.Limits.Contributors = 1
	p.record.Limits.AIModelClasses = []string{"standard"}
	if got := read(); got.EffectivePlan != "free" {
		t.Fatal(got)
	}
	p.record.Limits.AIModelClasses = []string{"fast", "standard"}
	admission.state.TodayUsed = 17
	got, err := s.Read(context.Background(), "caller", "", "")
	if err != nil || got.AI.Blocked == nil || *got.AI.Blocked != "daily" {
		t.Fatal(got, err)
	}
}

func TestPersonalPlanObservationPreservesCashGuard(t *testing.T) {
	s, _, _, usage, admission, _ := validPlanTestService()
	config := s.Config.(testConfigReader).config
	config.Enforced = false
	s.Config = testConfigReader{config: config}
	usage.usage = &models4datatug.AIUsageRecord{V: 1, PeriodID: "2026-03", Used: 7, Capped: true}
	admission.state.TodayUsed = 17
	read := func() PlanResponse {
		t.Helper()
		got, err := s.Read(context.Background(), "caller", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	observed := read()
	if observed.AI.Enforced || observed.AI.Left != 0 || observed.AI.Blocked == nil || *observed.AI.Blocked != "monthly" {
		t.Fatal(observed)
	}
	usage.usage.Capped = false // ordinary count exhaustion is only observed
	ordinary := read()
	if ordinary.AI.Blocked != nil || ordinary.AI.Left != 0 {
		t.Fatal(ordinary)
	}
	admission.state.Blocked = "free_budget"
	budget := read()
	if budget.AI.Blocked == nil || *budget.AI.Blocked != "free_budget" {
		t.Fatal(budget)
	}
	admission.state.Blocked = "unverified"
	unverified := read()
	if unverified.AI.Blocked == nil || *unverified.AI.Blocked != "unverified" {
		t.Fatal(unverified)
	}
	admission.state.Blocked = "daily"
	dailyObserved := read()
	if dailyObserved.AI.Blocked != nil {
		t.Fatal(dailyObserved)
	}
}

func TestPersonalPlanUsesAuthoritativeFirstAdmittedMonth(t *testing.T) {
	s, _, p, _, _, clock := validPlanTestService()
	marker := s.FirstMonth.(*testFirstMonthReader)
	var configAccount string
	var configAt time.Time
	config := s.Config.(testConfigReader)
	config.observe = func(account string, at time.Time) { configAccount, configAt = account, at }
	s.Config = config
	read := func() PlanResponse {
		t.Helper()
		got, err := s.Read(context.Background(), "caller", "", "ignored-project")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	initial := read()
	if initial.AI.Limit != 7 || marker.product != "datatug" || marker.caller != "caller" || marker.account != "personal-1" || configAccount != "personal-1" || !configAt.Equal(clock.now) || clock.calls != 1 {
		t.Fatal(initial, marker, configAccount, configAt, clock.calls)
	}
	marker.month = "2026-03"
	first := read()
	if first.AI.Limit != 7 {
		t.Fatal(first)
	}
	clock.now = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	later := read()
	if later.AI.Limit != 4 || later.AI.PeriodID != "2026-04" || !later.AI.ResetsAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) || !configAt.Equal(clock.now) {
		t.Fatal(later, configAt)
	}
	marker.month = ""
	stillInitial := read() // a calendar rollover does not start the first-admitted month
	if stillInitial.AI.Limit != 7 {
		t.Fatal(stillInitial)
	}
	pro := config.config.ProLimits
	p.record = &models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", PaidUntil: timePtr(clock.now.Add(time.Hour)), Limits: &pro}
	marker.err = errors.New("marker down")
	proResponse := read()
	if proResponse.EffectivePlan != "pro" || marker.calls != 4 {
		t.Fatal(proResponse, marker.calls)
	}
	p.record.Status = "ended"
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	marker.err = nil
	for _, invalid := range []string{"2026-4", "later", "2026-05"} {
		marker.month = invalid
		if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
			t.Fatal(invalid, err)
		}
	}
	marker.month = "2026-03"
	ended := read()
	if ended.AI.Limit != 4 {
		t.Fatal(ended)
	}
}

func TestPersonalPlanCallerAdmissionBinding(t *testing.T) {
	s, _, plans, _, legacy, clock := validPlanTestService()
	caller := &testCallerAdmissionReader{state: AdmissionState{TodayUsed: 9}}
	s.CallerAdmission = caller
	clock.now = time.Date(2026, 3, 20, 10, 0, 0, 230000000, time.FixedZone("offset", 3600))
	got, err := s.Read(context.Background(), "caller", "personal-1", "")
	if err != nil || got.AI.Today.Used != 9 || caller.calls != 1 || caller.callerID != "caller" || caller.accountID != "personal-1" || !caller.instant.Equal(clock.now.UTC()) || caller.instant.Location() != time.UTC || len(legacy.instants) != 0 {
		t.Fatal(got, err, caller, legacy.instants)
	}
	if _, err := s.Read(context.Background(), "caller", "foreign", ""); !errors.Is(err, ErrForeignAccount) || caller.calls != 1 || len(plans.ids) != 1 {
		t.Fatal(err, caller.calls, plans.ids)
	}

	caller.err = errors.New("daily count unavailable")
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) || len(legacy.instants) != 0 {
		t.Fatal(err, legacy.instants)
	}
	caller.err = nil
	caller.state.TodayUsed = -1
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	caller.state.TodayUsed = 17
	got, err = s.Read(context.Background(), "caller", "", "")
	if err != nil || got.AI.Today.Used != 17 || got.AI.Blocked == nil || *got.AI.Blocked != "daily" {
		t.Fatal(got, err)
	}

	s.CallerAdmission, s.Admission = nil, nil
	if _, err := s.Read(context.Background(), "caller", "", ""); !errors.Is(err, ErrPlanUnavailable) {
		t.Fatal(err)
	}
	s.Admission = legacy // Published legacy consumers still use the old port.
	legacy.state.TodayUsed = 5
	got, err = s.Read(context.Background(), "caller", "", "")
	if err != nil || got.AI.Today.Used != 5 || len(legacy.instants) != 1 {
		t.Fatal(got, err, legacy.instants)
	}
}
