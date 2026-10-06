package models4datatug

import (
	"errors"
	"strings"
	"time"

	"github.com/dal-go/record"
)

const (
	FreeMoneySettingsCollection = "datatugFreeMoneySettings"
	FreeMoneyMonthCollection    = "datatugFreeMoneyMonths"
)

var ErrInvalidFreeMoneyKey = errors.New("invalid Free money key scope")

// NewFreeMoneySettingsKey selects one private configuration per mode and product.
// The caller must supply a server-resolved mode; no settings record is created here.
func NewFreeMoneySettingsKey(mode, product string) (*record.Key, error) {
	if err := validateFreeMoneyScope(mode, product); err != nil {
		return nil, err
	}
	return record.NewKeyWithID(FreeMoneySettingsCollection, privatePlanID("free-settings", mode, product)), nil
}

// NewFreeMoneyPersonMonthKey selects a verified person's private UTC-month counter.
// The caller must supply a server-verified user ID.
func NewFreeMoneyPersonMonthKey(mode, product, verifiedUserID, periodID string) (*record.Key, error) {
	if err := validateFreeMoneyScope(mode, product); err != nil {
		return nil, err
	}
	if verifiedUserID == "" || strings.TrimSpace(verifiedUserID) != verifiedUserID || !validFreeMoneyPeriod(periodID) {
		return nil, ErrInvalidFreeMoneyKey
	}
	return record.NewKeyWithID(FreeMoneyMonthCollection, privatePlanID("free-person", mode, product, verifiedUserID, periodID)), nil
}

// NewFreeMoneyGlobalMonthKey selects the shared private UTC-month counter.
func NewFreeMoneyGlobalMonthKey(mode, product, periodID string) (*record.Key, error) {
	if err := validateFreeMoneyScope(mode, product); err != nil {
		return nil, err
	}
	if !validFreeMoneyPeriod(periodID) {
		return nil, ErrInvalidFreeMoneyKey
	}
	return record.NewKeyWithID(FreeMoneyMonthCollection, privatePlanID("free-global", mode, product, periodID)), nil
}

func validateFreeMoneyScope(mode, product string) error {
	if (mode != "test" && mode != "live") || product != "datatug" {
		return ErrInvalidFreeMoneyKey
	}
	return nil
}

func validFreeMoneyPeriod(periodID string) bool {
	period, err := time.Parse("2006-01", periodID)
	return err == nil && period.Format("2006-01") == periodID
}
