package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// SaveMeal persists a meal record. If m.ID is empty it is generated from m.Timestamp.
// If m.LoggedAt is zero it is set to the current UTC time.
// Returns the stored record (with ID and LoggedAt populated).
func (s *Store) SaveMeal(m types.Meal) (types.Meal, error) {
	if m.ID == "" {
		m.ID = types.MealID(m.Timestamp)
	}
	if m.LoggedAt.IsZero() {
		m.LoggedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO meals (id, description, carbs_est, protein_est, fat_est, timestamp, logged_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Description, m.CarbsEst, m.ProteinEst, m.FatEst,
		m.Timestamp.UTC().Format(time.RFC3339),
		m.LoggedAt.UTC().Format(time.RFC3339),
		m.Notes,
	)
	if err != nil {
		return m, fmt.Errorf("store: saving meal: %w", err)
	}
	return m, nil
}

// GetMeal retrieves a meal by its ID. Returns an error if not found.
func (s *Store) GetMeal(id string) (types.Meal, error) {
	row := s.db.QueryRow(
		`SELECT id, description, carbs_est, protein_est, fat_est, timestamp, logged_at, notes
		 FROM meals WHERE id = ?`, id,
	)
	m, err := scanMeal(row)
	if err == sql.ErrNoRows {
		return m, fmt.Errorf("store: meal %q not found", id)
	}
	return m, err
}

// ListMeals returns all meals whose timestamp falls within [start, end], ascending.
func (s *Store) ListMeals(start, end time.Time) ([]types.Meal, error) {
	rows, err := s.db.Query(
		`SELECT id, description, carbs_est, protein_est, fat_est, timestamp, logged_at, notes
		 FROM meals WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp ASC`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing meals: %w", err)
	}
	defer rows.Close()

	var meals []types.Meal
	for rows.Next() {
		m, err := scanMeal(rows)
		if err != nil {
			return nil, err
		}
		meals = append(meals, m)
	}
	return meals, rows.Err()
}

func scanMeal(s scanner) (types.Meal, error) {
	var m types.Meal
	var ts, loggedAt string
	err := s.Scan(
		&m.ID, &m.Description, &m.CarbsEst, &m.ProteinEst, &m.FatEst,
		&ts, &loggedAt, &m.Notes,
	)
	if err != nil {
		return m, err
	}
	m.Timestamp, _ = time.Parse(time.RFC3339, ts)
	m.LoggedAt, _ = time.Parse(time.RFC3339, loggedAt)
	return m, nil
}
