package flightroute

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLookupReturnsAirportsAndCachesCallsign(t *testing.T) {
	requests := 0
	client := NewClient("https://route.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/KLM1023" {
			t.Fatalf("path = %q, want /KLM1023", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != userAgent {
			t.Fatalf("unexpected headers: %v", r.Header)
		}
		body := `{"response":{"flightroute":{"origin":{"iata_code":"AMS","icao_code":"EHAM","municipality":"Amsterdam","name":"Amsterdam Airport Schiphol"},"destination":{"iata_code":"LHR","icao_code":"EGLL","municipality":"London","name":"London Heathrow Airport"}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, callsign := range []string{" klm1023 ", "KLM1023"} {
		info, err := client.Lookup(context.Background(), callsign)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Origin.String(); got != "Amsterdam Airport Schiphol (AMS/EHAM)" {
			t.Fatalf("origin = %q", got)
		}
		if got := info.Destination.String(); got != "London Heathrow Airport (LHR/EGLL)" {
			t.Fatalf("destination = %q", got)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestLookupCachesUnknownCallsign(t *testing.T) {
	requests := 0
	client := NewClient("https://route.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
	})}
	for range 2 {
		info, err := client.Lookup(context.Background(), "UNKNOWN1")
		if err != nil || info.Origin.String() != "" || info.Destination.String() != "" {
			t.Fatalf("unexpected result: info=%+v error=%v", info, err)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
