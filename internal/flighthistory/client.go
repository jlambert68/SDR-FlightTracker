// Package flighthistory retrieves recent aircraft traces for display alongside
// positions received by the local SDR.
package flighthistory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	userAgent       = "SDR-FlightTracker/1.0 (+https://github.com/jlambert68/SDR-FlightTracker)"
	traceCacheTime  = 15 * time.Second
	maximumBodySize = 16 << 20
)

// Point is one position in an aircraft's externally sourced flight history.
type Point struct {
	Timestamp    time.Time `json:"timestamp"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	AltitudeFeet float64   `json:"altitudeFeet"`
}

type cachedTrace struct {
	points  []Point
	found   bool
	expires time.Time
}

type traceSource struct {
	primary       string
	fallback      string
	requiredStart time.Time
}

// Client retrieves readsb trace files from an ADSB.lol-compatible server.
// Trace files are briefly cached so position refreshes and multiple map clients
// do not repeat the same upstream request.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu    sync.Mutex
	cache map[string]cachedTrace
	now   func() time.Time
}

// NewClient creates a flight-history client. baseURL is the service root, for
// example https://adsb.lol.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 6 * time.Second},
		cache:   make(map[string]cachedTrace),
		now:     time.Now,
	}
}

// Lookup returns positions in [before-lookback, before). Points at or after
// before are excluded so the caller can reconcile the trace with locally
// received positions.
func (c *Client) Lookup(ctx context.Context, icao string, before time.Time, lookback time.Duration) ([]Point, error) {
	icao = strings.ToLower(strings.TrimSpace(icao))
	if !validICAO(icao) {
		return nil, fmt.Errorf("invalid ICAO address %q", icao)
	}
	if before.IsZero() {
		return nil, fmt.Errorf("history end time is required")
	}
	if lookback <= 0 {
		return []Point{}, nil
	}

	before = before.UTC()
	start := before.Add(-lookback)
	sources := c.traceSources(icao, start, before)
	points := make([]Point, 0, 128)
	var firstErr error
	loaded := false
	for _, source := range sources {
		trace, found, err := c.loadTrace(ctx, source.primary, icao)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
		if found {
			loaded = true
			points = append(points, trace...)
		}
		if source.fallback == "" || (found && traceCovers(trace, source.requiredStart)) {
			continue
		}
		fallbackTrace, fallbackFound, fallbackErr := c.loadTrace(ctx, source.fallback, icao)
		if fallbackErr != nil {
			if firstErr == nil {
				firstErr = fallbackErr
			}
			continue
		}
		if fallbackFound {
			loaded = true
			points = append(points, fallbackTrace...)
		}
	}
	if !loaded && firstErr != nil {
		return nil, firstErr
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp.Before(points[j].Timestamp) })
	filtered := make([]Point, 0, len(points))
	for _, point := range points {
		if point.Timestamp.Before(start) || !point.Timestamp.Before(before) {
			continue
		}
		if len(filtered) > 0 && samePoint(filtered[len(filtered)-1], point) {
			continue
		}
		filtered = append(filtered, point)
	}
	return filtered, nil
}

func (c *Client) traceSources(icao string, start, before time.Time) []traceSource {
	lastIncluded := before.Add(-time.Nanosecond)
	date := utcDate(start)
	lastDate := utcDate(lastIncluded)
	now := c.now().UTC()
	sources := make([]traceSource, 0, 2)
	for !date.After(lastDate) {
		if sameUTCDate(date, now) {
			requiredStart := start
			if date.After(requiredStart) {
				requiredStart = date
			}
			sources = append(sources, traceSource{
				primary:       fmt.Sprintf("%s/data/traces/%s/trace_recent_%s.json", c.BaseURL, icao[4:], icao),
				fallback:      fmt.Sprintf("%s/data/traces/%s/trace_full_%s.json", c.BaseURL, icao[4:], icao),
				requiredStart: requiredStart,
			})
		} else {
			sources = append(sources, traceSource{primary: fmt.Sprintf("%s/globe_history/%s/traces/%s/trace_full_%s.json", c.BaseURL, date.Format("2006/01/02"), icao[4:], icao)})
		}
		date = date.AddDate(0, 0, 1)
	}
	return sources
}

func (c *Client) loadTrace(ctx context.Context, traceURL, icao string) ([]Point, bool, error) {
	now := c.now()
	c.mu.Lock()
	for key, entry := range c.cache {
		if !now.Before(entry.expires) {
			delete(c.cache, key)
		}
	}
	if entry, ok := c.cache[traceURL]; ok {
		points := append([]Point(nil), entry.points...)
		c.mu.Unlock()
		return points, entry.found, nil
	}
	c.mu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		c.mu.Lock()
		c.cache[traceURL] = cachedTrace{points: []Point{}, found: false, expires: now.Add(traceCacheTime)}
		c.mu.Unlock()
		return nil, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("history request %s returned HTTP %s", request.URL.Host, response.Status)
	}

	points, err := decodeTrace(io.LimitReader(response.Body, maximumBodySize), icao)
	if err != nil {
		return nil, false, fmt.Errorf("decode history from %s: %w", request.URL.Host, err)
	}
	c.mu.Lock()
	c.cache[traceURL] = cachedTrace{points: append([]Point(nil), points...), found: true, expires: now.Add(traceCacheTime)}
	c.mu.Unlock()
	return points, true, nil
}

func decodeTrace(reader io.Reader, icao string) ([]Point, error) {
	var payload struct {
		ICAO      string            `json:"icao"`
		Timestamp float64           `json:"timestamp"`
		Trace     []json.RawMessage `json:"trace"`
	}
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload.ICAO != "" && !strings.EqualFold(payload.ICAO, icao) {
		return nil, fmt.Errorf("trace ICAO %q does not match %q", payload.ICAO, icao)
	}
	if !finite(payload.Timestamp) {
		return nil, fmt.Errorf("invalid trace timestamp")
	}

	points := make([]Point, 0, len(payload.Trace))
	for _, rawPoint := range payload.Trace {
		var values []json.RawMessage
		if err := json.Unmarshal(rawPoint, &values); err != nil || len(values) < 4 {
			continue
		}
		offset, offsetOK := jsonNumber(values[0])
		latitude, latitudeOK := jsonNumber(values[1])
		longitude, longitudeOK := jsonNumber(values[2])
		altitude, altitudeOK := traceAltitude(values)
		if !offsetOK || !latitudeOK || !longitudeOK || !altitudeOK || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
			continue
		}
		seconds := payload.Timestamp + offset
		wholeSeconds, fraction := math.Modf(seconds)
		points = append(points, Point{
			Timestamp:    time.Unix(int64(wholeSeconds), int64(math.Round(fraction*float64(time.Second)))).UTC(),
			Latitude:     latitude,
			Longitude:    longitude,
			AltitudeFeet: altitude,
		})
	}
	return points, nil
}

func traceAltitude(values []json.RawMessage) (float64, bool) {
	// The local decoder reports barometric altitude, which readsb stores at
	// index 3. Using it on both sides keeps the history-to-live join smooth.
	if altitude, ok := jsonNumber(values[3]); ok {
		return altitude, true
	}
	var label string
	if json.Unmarshal(values[3], &label) == nil && strings.EqualFold(label, "ground") {
		return 0, true
	}
	if len(values) > 10 {
		if altitude, ok := jsonNumber(values[10]); ok {
			return altitude, true
		}
	}
	return 0, false
}

func jsonNumber(raw json.RawMessage) (float64, bool) {
	if value := strings.TrimSpace(string(raw)); value == "" || value == "null" {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || !finite(value) {
		return 0, false
	}
	return value, true
}

func validICAO(icao string) bool {
	if len(icao) != 6 {
		return false
	}
	_, err := hex.DecodeString(icao)
	return err == nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func utcDate(value time.Time) time.Time {
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func sameUTCDate(left, right time.Time) bool {
	return utcDate(left).Equal(utcDate(right))
}

func samePoint(left, right Point) bool {
	return left.Timestamp.Equal(right.Timestamp) && left.Latitude == right.Latitude && left.Longitude == right.Longitude && left.AltitudeFeet == right.AltitudeFeet
}

func traceCovers(points []Point, requiredStart time.Time) bool {
	for _, point := range points {
		if !point.Timestamp.After(requiredStart) {
			return true
		}
	}
	return false
}
