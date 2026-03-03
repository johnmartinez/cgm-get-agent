package dexcom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// Client calls the Dexcom v3 data endpoints. It fetches a valid Bearer token from
// OAuthHandler before each request, triggering a transparent refresh when needed.
type Client struct {
	baseURL    string
	oauth      *OAuthHandler
	httpClient *http.Client
}

// NewClient creates a Dexcom API client from application config and an OAuthHandler.
func NewClient(cfg *config.Config, oauth *OAuthHandler) *Client {
	return &Client{
		baseURL:    BaseURL(cfg.Dexcom.Environment),
		oauth:      oauth,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetEGVs returns EGV records for the given time window.
// Returns WindowTooLargeError if end-start exceeds 30 days.
// Records are returned in the order the API provides them (typically ascending systemTime).
func (c *Client) GetEGVs(ctx context.Context, start, end time.Time) ([]types.EGVRecord, error) {
	if end.Sub(start) > maxWindowDays*24*time.Hour {
		days := int(end.Sub(start).Hours() / 24)
		return nil, &WindowTooLargeError{RequestedDays: days}
	}

	var resp egvsResponse
	if err := c.doJSON(ctx, c.baseURL+"/v3/users/self/egvs?"+dateParams(start, end).Encode(), &resp); err != nil {
		return nil, err
	}

	records := make([]types.EGVRecord, 0, len(resp.EGVs))
	for _, e := range resp.EGVs {
		r, err := convertEGV(e)
		if err != nil {
			return nil, fmt.Errorf("dexcom: converting EGV: %w", err)
		}
		records = append(records, r)
	}
	return records, nil
}

// GetEvents returns Dexcom-logged events (carbs, insulin, exercise, health) for the window.
func (c *Client) GetEvents(ctx context.Context, start, end time.Time) ([]types.DexcomEvent, error) {
	var resp eventsResponse
	if err := c.doJSON(ctx, c.baseURL+"/v3/users/self/events?"+dateParams(start, end).Encode(), &resp); err != nil {
		return nil, err
	}

	events := make([]types.DexcomEvent, 0, len(resp.Events))
	for _, e := range resp.Events {
		ev, err := convertEvent(e)
		if err != nil {
			return nil, fmt.Errorf("dexcom: converting event: %w", err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// GetDataRange returns the earliest and latest record timestamps for each data type.
// Useful for determining whether new data has been uploaded since the last fetch.
func (c *Client) GetDataRange(ctx context.Context) (types.DataRange, error) {
	var resp dataRangeResponse
	if err := c.doJSON(ctx, c.baseURL+"/v3/users/self/dataRange", &resp); err != nil {
		return types.DataRange{}, err
	}

	return types.DataRange{
		Calibrations: types.TimeRange{
			Start: parseTime(resp.Calibrations.Start),
			End:   parseTime(resp.Calibrations.End),
		},
		EGVs: types.TimeRange{
			Start: parseTime(resp.EGVs.Start),
			End:   parseTime(resp.EGVs.End),
		},
		Events: types.TimeRange{
			Start: parseTime(resp.Events.Start),
			End:   parseTime(resp.Events.End),
		},
	}, nil
}

// GetDevices returns G7 device and transmitter information for the authenticated user.
func (c *Client) GetDevices(ctx context.Context) ([]DeviceRecord, error) {
	var resp devicesResponse
	if err := c.doJSON(ctx, c.baseURL+"/v3/users/self/devices", &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// doJSON performs a GET with a Bearer token and JSON-decodes the response into dst.
// Returns AuthError on 401, APIError on other non-2xx responses.
func (c *Client) doJSON(ctx context.Context, endpoint string, dst any) error {
	token, err := c.oauth.GetValidToken(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dexcom: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dexcom: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return &AuthError{Message: "access token rejected — re-authorization may be required"}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("dexcom: decoding response: %w", err)
	}
	return nil
}

// dateParams builds the startDate/endDate query string in Dexcom timestamp format.
func dateParams(start, end time.Time) url.Values {
	return url.Values{
		"startDate": {start.UTC().Format(dexcomTimeFormat)},
		"endDate":   {end.UTC().Format(dexcomTimeFormat)},
	}
}

// parseTime tries Dexcom format first, then RFC3339. Returns zero time on failure.
func parseTime(s string) time.Time {
	if t, err := time.Parse(dexcomTimeFormat, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func convertEGV(e apiEGV) (types.EGVRecord, error) {
	sys, err := time.Parse(dexcomTimeFormat, e.SystemTime)
	if err != nil {
		return types.EGVRecord{}, fmt.Errorf("parsing systemTime %q: %w", e.SystemTime, err)
	}
	disp, _ := time.Parse(dexcomTimeFormat, e.DisplayTime)
	return types.EGVRecord{
		RecordID:              e.RecordID,
		SystemTime:            sys,
		DisplayTime:           disp,
		TransmitterID:         e.TransmitterID,
		TransmitterTicks:      e.TransmitterTicks,
		Value:                 e.Value,
		Trend:                 types.TrendArrow(e.Trend),
		TrendRate:             e.TrendRate,
		Unit:                  e.Unit,
		RateUnit:              e.RateUnit,
		DisplayDevice:         e.DisplayDevice,
		TransmitterGeneration: e.TransmitterGeneration,
		DisplayApp:            e.DisplayApp,
	}, nil
}

func convertEvent(e apiEvent) (types.DexcomEvent, error) {
	sys, err := time.Parse(dexcomTimeFormat, e.SystemTime)
	if err != nil {
		return types.DexcomEvent{}, fmt.Errorf("parsing event systemTime %q: %w", e.SystemTime, err)
	}
	disp, _ := time.Parse(dexcomTimeFormat, e.DisplayTime)
	return types.DexcomEvent{
		RecordID:     e.RecordID,
		SystemTime:   sys,
		DisplayTime:  disp,
		EventType:    types.EventType(e.EventType),
		EventSubType: e.EventSubType,
		Value:        e.Value,
		Unit:         e.Unit,
	}, nil
}
