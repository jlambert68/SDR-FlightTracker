package flightroute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "SDR-FlightTracker/1.0 (+https://github.com/jlambert68/SDR-FlightTracker)"

// Airport is an endpoint in a published callsign route.
type Airport struct {
	Name         string `json:"name"`
	Municipality string `json:"municipality"`
	IATA         string `json:"iata_code"`
	ICAO         string `json:"icao_code"`
}

// String returns a compact airport name with all available codes.
func (a Airport) String() string {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = strings.TrimSpace(a.Municipality)
	}
	codes := make([]string, 0, 2)
	if code := strings.ToUpper(strings.TrimSpace(a.IATA)); code != "" {
		codes = append(codes, code)
	}
	if code := strings.ToUpper(strings.TrimSpace(a.ICAO)); code != "" {
		codes = append(codes, code)
	}
	if name == "" {
		return strings.Join(codes, "/")
	}
	if len(codes) == 0 {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, strings.Join(codes, "/"))
}

// Info contains the departure and destination associated with a callsign.
type Info struct {
	Origin      Airport
	Destination Airport
}

// Client caches callsign lookups for the lifetime of the receiver process.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	mu      sync.Mutex
	cache   map[string]Info
	errors  map[string]error
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 3 * time.Second},
		cache:   make(map[string]Info),
		errors:  make(map[string]error),
	}
}

// Lookup gets the published route for an ICAO or IATA callsign. An unknown
// callsign returns an empty Info without an error.
func (c *Client) Lookup(ctx context.Context, callsign string) (Info, error) {
	callsign = strings.ToUpper(strings.TrimSpace(callsign))
	if callsign == "" {
		return Info{}, nil
	}
	c.mu.Lock()
	if value, ok := c.cache[callsign]; ok {
		err := c.errors[callsign]
		c.mu.Unlock()
		return value, err
	}
	c.mu.Unlock()

	value, lookupErr := c.lookup(ctx, callsign)
	c.mu.Lock()
	c.cache[callsign] = value
	c.errors[callsign] = lookupErr
	c.mu.Unlock()
	return value, lookupErr
}

func (c *Client) lookup(ctx context.Context, callsign string) (Info, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+url.PathEscape(callsign), nil)
	if err != nil {
		return Info{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return Info{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Info{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("HTTP %s", response.Status)
	}
	var payload struct {
		Response struct {
			FlightRoute struct {
				Origin      Airport `json:"origin"`
				Destination Airport `json:"destination"`
			} `json:"flightroute"`
		} `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Info{}, err
	}
	return Info{Origin: payload.Response.FlightRoute.Origin, Destination: payload.Response.FlightRoute.Destination}, nil
}
