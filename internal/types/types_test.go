package types_test

import (
	"strings"
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

func TestMealID_Format(t *testing.T) {
	ts := time.Date(2026, 3, 3, 12, 15, 0, 0, time.UTC)
	id := types.MealID(ts)
	if id != "m_20260303_1215" {
		t.Errorf("MealID: expected %q, got %q", "m_20260303_1215", id)
	}
	if !strings.HasPrefix(id, "m_") {
		t.Errorf("MealID must start with m_, got %q", id)
	}
}

func TestExerciseID_Format(t *testing.T) {
	ts := time.Date(2026, 3, 3, 14, 30, 0, 0, time.UTC)
	id := types.ExerciseID(ts)
	if id != "e_20260303_1430" {
		t.Errorf("ExerciseID: expected %q, got %q", "e_20260303_1430", id)
	}
	if !strings.HasPrefix(id, "e_") {
		t.Errorf("ExerciseID must start with e_, got %q", id)
	}
}

func TestMealID_UsesUTC(t *testing.T) {
	// A non-UTC time should still produce a UTC-based ID.
	loc, _ := time.LoadLocation("America/Los_Angeles")
	ts := time.Date(2026, 3, 3, 4, 0, 0, 0, loc) // 04:00 PT = 12:00 UTC
	id := types.MealID(ts)
	if id != "m_20260303_1200" {
		t.Errorf("MealID must use UTC: expected %q, got %q", "m_20260303_1200", id)
	}
}

func TestExerciseID_UsesUTC(t *testing.T) {
	loc, _ := time.LoadLocation("America/Los_Angeles")
	ts := time.Date(2026, 3, 3, 7, 30, 0, 0, loc) // 07:30 PT = 15:30 UTC
	id := types.ExerciseID(ts)
	if id != "e_20260303_1530" {
		t.Errorf("ExerciseID must use UTC: expected %q, got %q", "e_20260303_1530", id)
	}
}
