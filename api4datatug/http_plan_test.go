package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
)

type planVerifier struct {
	caller string
	err    error
}

func (v planVerifier) VerifyPlanCaller(context.Context, *http.Request) (string, error) {
	return v.caller, v.err
}

type planDirectory struct{ calls int }

func (d *planDirectory) PersonalAccount(_ context.Context, caller string) (facade4datatug.PersonalAccount, error) {
	d.calls++
	if caller != "user-1" {
		return facade4datatug.PersonalAccount{}, errors.New("caller")
	}
	return facade4datatug.PersonalAccount{ID: "personal-1", Title: "Example"}, nil
}

type planStore struct {
	record *models4datatug.PlanRecord
	ids    []string
}

func (p *planStore) ReadCurrentPlan(_ context.Context, id string) (*models4datatug.PlanRecord, error) {
	p.ids = append(p.ids, id)
	return p.record, nil
}

type usageStore struct{ ids []string }

func (u *usageStore) ReadUsage(_ context.Context, id, period string) (*models4datatug.AIUsageRecord, error) {
	u.ids = append(u.ids, id+"/"+period)
	return nil, nil
}

type planAdmission struct{}

func (planAdmission) ReadAdmission(context.Context, string, time.Time) (facade4datatug.AdmissionState, error) {
	return facade4datatug.AdmissionState{}, nil
}

type planSettings struct{ config facade4datatug.PlanConfig }

func (s planSettings) ReadPlanConfig(context.Context) (facade4datatug.PlanConfig, error) {
	return s.config, nil
}

type planTime struct{ now time.Time }

func (c planTime) Now() time.Time { return c.now }

func TestPlanHTTPFreeProEndedAndForeignAccount(t *testing.T) {
	five := int64(5)
	free := models4datatug.PlanLimits{Contributors: 1, ProjectGuests: 2, AIQuestions: 7, AIModelClasses: []string{"fast"}, AIPaysFor: "owner"}
	pro := models4datatug.PlanLimits{Contributors: 1, ProjectGuests: 3, ProjectContributors: &five, AIQuestions: 11, AIModelClasses: []string{"fast"}, AIPaysFor: "owner"}
	config := facade4datatug.PlanConfig{FreeLimits: free, ProLimits: pro, FreeModels: []facade4datatug.PlanModel{{ID: "model", Class: "fast", Weight: 1, Default: true}}, ProModels: []facade4datatug.PlanModel{{ID: "model", Class: "fast", Weight: 1, Default: true}}, DailyLimit: 17, ActiveGrace: time.Hour, PastDueGrace: 72 * time.Hour, Enforced: true, UpgradeURL: "https://example.com/pro"}
	d := &planDirectory{}
	p := &planStore{}
	u := &usageStore{}
	now := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	service := facade4datatug.PersonalPlanService{Directory: d, Plans: p, Usage: u, Admission: planAdmission{}, Config: planSettings{config}, Clock: planTime{now}}
	routes := map[string]http.HandlerFunc{}
	RegisterHttpRoutesWithPlan(func(method, path string, h http.HandlerFunc) { routes[method+" "+path] = h }, fakeIDs{}, PlanRouteOptions{Verifier: planVerifier{caller: "user-1"}, Service: service})
	handler := routes["GET /v0/datatug/plan"]
	if handler == nil {
		t.Fatal("plan route missing")
	}
	read := func(path string) (int, facade4datatug.PlanResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		var out facade4datatug.PlanResponse
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, out
	}
	status, freeResponse := read("/v0/datatug/plan?project=foreign")
	if status != 200 || freeResponse.Plan != "free" || freeResponse.AccountID != "personal-1" || len(freeResponse.Accounts) != 1 {
		t.Fatal(status, freeResponse)
	}
	facts := facade4datatug.PlanFacts{AccountKind: "personal", Tier: "pro", Period: "month", ProviderStatus: "active", PaidUntil: &now, Grants: &pro}
	p.record = facade4datatug.PlanStateFor(facts).Record
	status, proResponse := read("/v0/datatug/plan?account=personal-1")
	if status != 200 || proResponse.Plan != "pro" || proResponse.Limits.ProjectContributors == nil || *proResponse.Limits.ProjectContributors != 5 {
		t.Fatal(status, proResponse)
	}
	encoded, err := json.Marshal(proResponse)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	limits := wire["limits"].(map[string]any)
	if limits["projectContributors"] != float64(5) {
		t.Fatal(limits)
	}
	facts.ProviderStatus = "canceled"
	p.record = facade4datatug.PlanStateFor(facts).Record
	status, ended := read("/v0/datatug/plan")
	if status != 200 || ended.Plan != "free" || ended.Status != "ended" || ended.Limits.ProjectContributors != nil {
		t.Fatal(status, ended)
	}
	before := len(p.ids)
	status, _ = read("/v0/datatug/plan?account=foreign")
	if status != 403 || len(p.ids) != before || len(u.ids) != 3 {
		t.Fatal(status, p.ids, u.ids)
	}
	if d.calls != 4 {
		t.Fatal(d.calls)
	}
}

func TestPlanHTTPRequiresHostVerifierAndDependencies(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v0/datatug/plan", nil)
	httpGetPlan(PlanRouteOptions{})(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	httpGetPlan(PlanRouteOptions{Verifier: planVerifier{err: errors.New("expired")}})(w, r)
	if w.Code != 401 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	httpGetPlan(PlanRouteOptions{Verifier: planVerifier{caller: "user-1"}})(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
