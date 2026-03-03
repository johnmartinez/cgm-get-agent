package store

import (
	"fmt"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// SaveExercise persists an exercise session. ID and LoggedAt are auto-populated if zero.
func (s *Store) SaveExercise(e types.Exercise) (types.Exercise, error) {
	if e.ID == "" {
		e.ID = types.ExerciseID(e.Timestamp)
	}
	if e.LoggedAt.IsZero() {
		e.LoggedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO exercise (id, type, duration_min, intensity, timestamp, logged_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.DurationMin, string(e.Intensity),
		e.Timestamp.UTC().Format(time.RFC3339),
		e.LoggedAt.UTC().Format(time.RFC3339),
		e.Notes,
	)
	if err != nil {
		return e, fmt.Errorf("store: saving exercise: %w", err)
	}
	return e, nil
}

// ListExercise returns all exercise sessions whose timestamp falls within [start, end], ascending.
func (s *Store) ListExercise(start, end time.Time) ([]types.Exercise, error) {
	rows, err := s.db.Query(
		`SELECT id, type, duration_min, intensity, timestamp, logged_at, notes
		 FROM exercise WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp ASC`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing exercise: %w", err)
	}
	defer rows.Close()

	var exercises []types.Exercise
	for rows.Next() {
		e, err := scanExercise(rows)
		if err != nil {
			return nil, err
		}
		exercises = append(exercises, e)
	}
	return exercises, rows.Err()
}

func scanExercise(s scanner) (types.Exercise, error) {
	var e types.Exercise
	var intensity, ts, loggedAt string
	err := s.Scan(&e.ID, &e.Type, &e.DurationMin, &intensity, &ts, &loggedAt, &e.Notes)
	if err != nil {
		return e, fmt.Errorf("store: scanning exercise: %w", err)
	}
	e.Intensity = types.ExerciseIntensity(intensity)
	e.Timestamp, _ = time.Parse(time.RFC3339, ts)
	e.LoggedAt, _ = time.Parse(time.RFC3339, loggedAt)
	return e, nil
}
