package models4datatug

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/dal-go/record"
)

func TestFreeMoneyKeyGoldenPathsAndScopes(t *testing.T) {
	settings := mustSettingsKey(t, "live", "datatug")
	person := mustPersonKey(t, "live", "datatug", "uid-123", "2026-10")
	global := mustGlobalKey(t, "live", "datatug", "2026-10")
	want := map[string]string{
		settings.String(): "datatugFreeMoneySettings/v1-9373cefe985421e10166654b6671b80097cb7aac5e92b478841394765edc0bff",
		person.String():   "datatugFreeMoneyMonths/v1-1168c66f6b75a5f39984f5b7a24115c764fa22605afd94df99b11374c4529475",
		global.String():   "datatugFreeMoneyMonths/v1-998f9f0155f942ca875dd0b7511ce17d589900e5cbada19ebaa95c3e94bdf702",
	}
	if len(want) != 3 {
		t.Fatal("settings, person and global keys must be distinct")
	}
	for got, expected := range want {
		if got != expected {
			t.Fatalf("golden path changed: got %s, want %s", got, expected)
		}
	}
	for _, key := range []*record.Key{settings, person, global} {
		id, ok := key.ID.(string)
		if key.Parent() != nil || !ok || !regexp.MustCompile(`^v1-[a-f0-9]{64}$`).MatchString(id) {
			t.Fatalf("private key must be a root with an opaque v1 ID: %s", key)
		}
		for _, sensitive := range []string{"uid-123", "2026-10", "live", "datatug"} {
			if strings.Contains(id, sensitive) {
				t.Fatalf("private key leaks %q: %s", sensitive, key)
			}
		}
	}
	if settings.Collection() != FreeMoneySettingsCollection || person.Collection() != FreeMoneyMonthCollection || global.Collection() != FreeMoneyMonthCollection {
		t.Fatal("unexpected Free money root collection")
	}
	if again := mustSettingsKey(t, "live", "datatug"); again.String() != settings.String() {
		t.Fatal("settings key changed for identical input")
	}
	if again := mustPersonKey(t, "live", "datatug", "uid-123", "2026-10"); again.String() != person.String() {
		t.Fatal("person key changed for identical input")
	}
	if again := mustGlobalKey(t, "live", "datatug", "2026-10"); again.String() != global.String() {
		t.Fatal("global key changed for identical input")
	}
}

func TestFreeMoneyKeySeparation(t *testing.T) {
	settingsLive := mustSettingsKey(t, "live", "datatug")
	settingsTest := mustSettingsKey(t, "test", "datatug")
	if settingsLive.String() == settingsTest.String() {
		t.Fatal("settings mode collision")
	}
	personA := mustPersonKey(t, "live", "datatug", "uid-123", "2026-10")
	personB := mustPersonKey(t, "live", "datatug", "uid-456", "2026-10")
	personNext := mustPersonKey(t, "live", "datatug", "uid-123", "2026-11")
	personTest := mustPersonKey(t, "test", "datatug", "uid-123", "2026-10")
	global := mustGlobalKey(t, "live", "datatug", "2026-10")
	globalNext := mustGlobalKey(t, "live", "datatug", "2026-11")
	globalTest := mustGlobalKey(t, "test", "datatug", "2026-10")
	for _, other := range []*record.Key{personB, personNext, personTest, global} {
		if personA.String() == other.String() {
			t.Fatal("person counter scope collision", other)
		}
	}
	if global.String() == globalNext.String() || global.String() == globalTest.String() {
		t.Fatal("global counter month/mode collision")
	}
	// Every person in one mode/product/month must share this one global key.
	if globalForB := mustGlobalKey(t, "live", "datatug", "2026-10"); globalForB.String() != global.String() {
		t.Fatal("people address different global counters")
	}
	if privatePlanID("ab", "c") == privatePlanID("a", "bc") ||
		privatePlanID("free-person", "live", "datatug", "a", "b-c") == privatePlanID("free-person", "live", "datatug", "a-b", "c") {
		t.Fatal("length-prefixed components collided")
	}
}

func TestFreeMoneyKeysRefuseInvalidScope(t *testing.T) {
	settings := []struct{ mode, product string }{
		{"", "datatug"}, {"Live", "datatug"}, {"prod", "datatug"}, {"test ", "datatug"},
		{"live", ""}, {"live", "other"}, {"live", "datatug "},
	}
	for _, input := range settings {
		key, err := NewFreeMoneySettingsKey(input.mode, input.product)
		assertInvalidFreeMoneyKey(t, key, err)
		key, err = NewFreeMoneyPersonMonthKey(input.mode, input.product, "uid-123", "2026-10")
		assertInvalidFreeMoneyKey(t, key, err)
		key, err = NewFreeMoneyGlobalMonthKey(input.mode, input.product, "2026-10")
		assertInvalidFreeMoneyKey(t, key, err)
	}
	for _, userID := range []string{"", " ", "\t", " uid", "uid "} {
		key, err := NewFreeMoneyPersonMonthKey("live", "datatug", userID, "2026-10")
		assertInvalidFreeMoneyKey(t, key, err)
	}
	for _, periodID := range []string{"", "2026-1", "2026-00", "2026-13", "2026-10-01", " 2026-10", "2026-10 ", "2026-10Z"} {
		key, err := NewFreeMoneyPersonMonthKey("live", "datatug", "uid-123", periodID)
		assertInvalidFreeMoneyKey(t, key, err)
		key, err = NewFreeMoneyGlobalMonthKey("live", "datatug", periodID)
		assertInvalidFreeMoneyKey(t, key, err)
	}
}

func mustFreeMoneyKey(t *testing.T, key *record.Key, err error) *record.Key {
	t.Helper()
	if err != nil || key == nil {
		t.Fatalf("expected valid key, got %v, %v", key, err)
	}
	return key
}

func assertInvalidFreeMoneyKey(t *testing.T, key *record.Key, err error) {
	t.Helper()
	if key != nil || !errors.Is(err, ErrInvalidFreeMoneyKey) {
		t.Fatalf("invalid scope returned key=%v, err=%v", key, err)
	}
}

func mustSettingsKey(t *testing.T, mode, product string) *record.Key {
	t.Helper()
	key, err := NewFreeMoneySettingsKey(mode, product)
	return mustFreeMoneyKey(t, key, err)
}

func mustPersonKey(t *testing.T, mode, product, userID, periodID string) *record.Key {
	t.Helper()
	key, err := NewFreeMoneyPersonMonthKey(mode, product, userID, periodID)
	return mustFreeMoneyKey(t, key, err)
}

func mustGlobalKey(t *testing.T, mode, product, periodID string) *record.Key {
	t.Helper()
	key, err := NewFreeMoneyGlobalMonthKey(mode, product, periodID)
	return mustFreeMoneyKey(t, key, err)
}
