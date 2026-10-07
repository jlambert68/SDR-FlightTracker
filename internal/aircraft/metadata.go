package aircraft

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Info is the human-readable identity returned by an aircraft database.
type Info struct {
	Registration string `json:"Registration"`
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Type"`
	TypeCode     string `json:"ICAOTypeCode"`
	Operator     string `json:"RegisteredOwners"`
	Country      string `json:"Country"`
}

func (i Info) String() string {
	parts := make([]string, 0, 6)
	if i.Registration != "" {
		parts = append(parts, "registration="+i.Registration)
	}
	if i.Manufacturer != "" {
		parts = append(parts, "manufacturer="+i.Manufacturer)
	}
	if i.Model != "" {
		parts = append(parts, "model="+i.Model)
	}
	if i.TypeCode != "" {
		parts = append(parts, "typecode="+i.TypeCode)
	}
	if i.Operator != "" {
		parts = append(parts, "operator="+i.Operator)
	}
	if i.Country != "" {
		parts = append(parts, "country="+i.Country)
	}
	return strings.Join(parts, " ")
}

// Client caches one lookup per ICAO address to avoid repeatedly querying the service.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	mu      sync.Mutex
	cache   map[string]string
	errors  map[string]error
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 3 * time.Second}, cache: make(map[string]string), errors: make(map[string]error)}
}

func (c *Client) Lookup(ctx context.Context, icao string) (string, error) {
	icao = strings.ToLower(icao)
	c.mu.Lock()
	if value, ok := c.cache[icao]; ok {
		err := c.errors[icao]
		c.mu.Unlock()
		return value, err
	}
	c.mu.Unlock()

	value := ""
	var lookupErr error
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+icao, nil)
	if err == nil {
		response, requestErr := c.HTTP.Do(req)
		if requestErr != nil {
			lookupErr = requestErr
		} else {
			defer response.Body.Close()
			if response.StatusCode == http.StatusOK {
				var info Info
				if decodeErr := json.NewDecoder(response.Body).Decode(&info); decodeErr == nil {
					value = info.String()
				} else {
					lookupErr = decodeErr
				}
			} else {
				lookupErr = fmt.Errorf("HTTP %s", response.Status)
			}
		}
	} else {
		lookupErr = err
	}
	c.mu.Lock()
	c.cache[icao] = value
	c.errors[icao] = lookupErr
	c.mu.Unlock()
	return value, lookupErr
}
