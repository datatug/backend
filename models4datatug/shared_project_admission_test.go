// Copyright 2026 Sneat.co
package models4datatug

import "testing"

func TestProjectQuotaLimitRequiresExplicitUnlimited(t *testing.T) {
	for _, v := range []struct {
		limit     ProjectQuotaLimit
		allocated int64
		want      bool
	}{{ProjectQuotaLimit{}, 0, false}, {ProjectQuotaLimit{Count: 5}, 4, true}, {ProjectQuotaLimit{Count: 5}, 5, false}, {ProjectQuotaLimit{Unlimited: true}, 500, true}, {ProjectQuotaLimit{Unlimited: true, Count: 5}, 0, false}, {ProjectQuotaLimit{Count: -1}, 0, false}, {ProjectQuotaLimit{Unlimited: true}, -1, false}} {
		if got := v.limit.Allows(v.allocated); got != v.want {
			t.Fatalf("limit %+v allocated %d = %v", v.limit, v.allocated, got)
		}
	}
}
func TestProjectQuotaKeysIsolateModeProductPayerAndSpace(t *testing.T) {
	seen := map[string]bool{}
	for _, mode := range []string{"live", "test"} {
		for _, product := range []string{"datatug", "other"} {
			for _, payer := range []string{"p1", "p2"} {
				r, _ := NewProtectedProjectQuotaRecord(mode, product, payer)
				key := r.Key().String()
				if seen[key] {
					t.Fatal("key collision", key)
				}
				seen[key] = true
			}
		}
	}
	a, _ := NewProjectAdmissionRecord("space-1", "same")
	b, _ := NewProjectAdmissionRecord("space-2", "same")
	if a.Key().String() == b.Key().String() {
		t.Fatal("project scope collision")
	}
}
