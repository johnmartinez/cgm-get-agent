package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// CacheEGVs upserts EGV records into glucose_cache using record_id as the dedup key.
// Returns the number of records successfully written.
func (s *Store) CacheEGVs(records []types.EGVRecord) (int, error) {
	count := 0
	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			return count, fmt.Errorf("store: marshaling egv %s: %w", r.RecordID, err)
		}
		_, err = s.db.Exec(
			`INSERT OR REPLACE INTO glucose_cache
			 (record_id, system_time, display_time, value, trend, trend_rate, raw_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.RecordID,
			r.SystemTime.UTC().Format(time.RFC3339),
			r.DisplayTime.UTC().Format(time.RFC3339),
			r.Value,
			string(r.Trend),
			r.TrendRate,
			string(raw),
		)
		if err != nil {
			return count, fmt.Errorf("store: caching egv %s: %w", r.RecordID, err)
		}
		count++
	}
	return count, nil
}

// GetCachedEGVs retrieves EGV records from the cache with system_time in [start, end],
// ordered ascending by system_time.
func (s *Store) GetCachedEGVs(start, end time.Time) ([]types.EGVRecord, error) {
	rows, err := s.db.Query(
		`SELECT raw_json FROM glucose_cache
		 WHERE system_time >= ? AND system_time <= ?
		 ORDER BY system_time ASC`,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store: querying cached egvs: %w", err)
	}
	defer rows.Close()

	var egvs []types.EGVRecord
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store: scanning cached egv: %w", err)
		}
		var r types.EGVRecord
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("store: unmarshaling cached egv: %w", err)
		}
		egvs = append(egvs, r)
	}
	return egvs, rows.Err()
}
