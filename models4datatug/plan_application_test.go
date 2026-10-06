package models4datatug

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivatePlanKeysAndPayload(t *testing.T) {
	app := NewPlanApplicationKey("live", "datatug", "space-A")
	month := NewPaidMoneyMonthKey("live", "datatug", "space-A", "2026-10")
	for _, other := range []string{
		NewPlanApplicationKey("test", "datatug", "space-A").String(),
		NewPlanApplicationKey("live", "other", "space-A").String(),
		NewPlanApplicationKey("live", "datatug", "space-B").String(),
		month.String(),
	} {
		if app.String() == other {
			t.Fatal("private key collision", other)
		}
	}
	if strings.Contains(app.String(), "/spaces/") || strings.Contains(month.String(), "/spaces/") ||
		strings.Contains(app.String(), "/users/") || strings.Contains(month.String(), "/users/") {
		t.Fatal(app, month)
	}
	for _, v := range []any{
		PlanApplication{V: 1, Mode: "live", Family: "datatug", AccountID: "space-A"},
		PaidMoneyMonth{V: 1, Mode: "live", AccountID: "space-A", PeriodID: "2026-10"},
	} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["userIDs"]; ok {
			t.Fatal(string(data))
		}
		if _, ok := fields["spaceIDs"]; ok {
			t.Fatal(string(data))
		}
	}
}
