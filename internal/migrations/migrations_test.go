package migrations

import (
	"errors"
	"testing"
)

func TestCheckSupported_InRange(t *testing.T) {
	if err := CheckSupported(1); err != nil {
		t.Errorf("version 1 should be supported: %v", err)
	}
}

func TestCheckSupported_TooNew(t *testing.T) {
	err := CheckSupported(2)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("want ErrSchemaTooNew, got %v", err)
	}
}

func TestCheckSupported_TooOld(t *testing.T) {
	err := CheckSupported(0)
	if !errors.Is(err, ErrSchemaTooOld) {
		t.Errorf("want ErrSchemaTooOld, got %v", err)
	}
}

func TestApplyMigrations_SameVersion(t *testing.T) {
	fm := map[string]any{"x": "y"}
	body := "body"
	gotFm, gotBody, err := ApplyMigrations(fm, body, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotFm["x"] != "y" || gotBody != "body" {
		t.Error("pass-through on same version should be identity")
	}
}

func TestApplyMigrations_NoRegisteredPath(t *testing.T) {
	_, _, err := ApplyMigrations(nil, "", 1, 1)
	if err != nil {
		t.Fatalf("same-version should succeed: %v", err)
	}
	// When a hypothetical future schema 2 is requested against empty
	// registry, CheckSupported rejects first. Bypass by temporarily
	// widening the range to confirm the registry-miss branch.
	orig := SupportedRange
	SupportedRange = [2]int{1, 2}
	defer func() { SupportedRange = orig }()

	_, _, err = ApplyMigrations(nil, "", 1, 2)
	if !errors.Is(err, ErrNoMigrationPath) {
		t.Errorf("want ErrNoMigrationPath, got %v", err)
	}
}
