package flighthistory

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupLoadsArchivedTraceAndTrimsToLookback(t *testing.T) {
	before := time.Date(2026, 10, 8, 19, 10, 0, 0, time.UTC)
	var requests atomic.Int32
	client := NewClient("https://history.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if got, want := r.URL.Path, "/globe_history/2026/10/08/traces/10/trace_full_4d2410.json"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != userAgent {
			t.Fatalf("unexpected headers: %v", r.Header)
		}
		return jsonResponse(`{
			"icao":"4d2410",
			"timestamp":1791486000,
			"trace":[
				[299,59.0,18.0,9000,200,90,0,0,null,"adsb_icao",9100],
				[300,59.1,18.1,9200,200,90,0,0,null,"adsb_icao",9300],
				[450,59.2,18.2,9400,200,90,0,0,null,"adsb_icao",9550],
				[500,59.25,18.25,null,200,90,0,0,null,"adsb_icao",9650],
				[599.5,59.3,18.3,"ground",10,90,0,0,null,"adsb_icao",null],
				[600,59.4,18.4,9700,200,90,0,0,null,"adsb_icao",9800]
			]
		}`), nil
	})}

	client.now = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
	for range 2 {
		points, err := client.Lookup(context.Background(), "4D2410", before, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(points) != 4 {
			t.Fatalf("points = %+v, want 4", points)
		}
		if got := points[0].Timestamp; !got.Equal(before.Add(-5 * time.Minute)) {
			t.Fatalf("first timestamp = %s", got)
		}
		if points[0].AltitudeFeet != 9200 || points[1].AltitudeFeet != 9400 {
			t.Fatalf("barometric altitude was not preferred: %+v", points)
		}
		if points[2].AltitudeFeet != 9650 {
			t.Fatalf("geometric altitude fallback = %v, want 9650", points[2].AltitudeFeet)
		}
		if points[3].AltitudeFeet != 0 {
			t.Fatalf("ground altitude = %v, want 0", points[3].AltitudeFeet)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1 cached request", got)
	}
}

func TestLookupUsesRecentTraceForLiveAircraft(t *testing.T) {
	before := time.Date(2026, 10, 11, 19, 12, 40, 0, time.UTC)
	client := NewClient("https://history.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.Path, "/data/traces/b5/trace_recent_4cacb5.json"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		return jsonResponse(`{"icao":"4cacb5","timestamp":1791745660,"trace":[[0,59.2,17.8,7000]]}`), nil
	})}

	client.now = func() time.Time { return before.Add(2 * time.Second) }
	points, err := client.Lookup(context.Background(), "4CACB5", before, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Latitude != 59.2 || points[0].AltitudeFeet != 7000 {
		t.Fatalf("unexpected live history: %+v", points)
	}
}

func TestLookupFallsBackToFullLiveTraceWhenRecentTraceIsTooShort(t *testing.T) {
	before := time.Date(2026, 10, 11, 19, 12, 40, 0, time.UTC)
	requests := make([]string, 0, 2)
	client := NewClient("https://history.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/data/traces/b5/trace_recent_4cacb5.json":
			return jsonResponse(`{"icao":"4cacb5","timestamp":1791745900,"trace":[[0,59.2,17.8,7000]]}`), nil
		case "/data/traces/b5/trace_full_4cacb5.json":
			return jsonResponse(`{"icao":"4cacb5","timestamp":1791745600,"trace":[[60,59.1,17.7,7100]]}`), nil
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
			return nil, nil
		}
	})}
	client.now = func() time.Time { return before }

	points, err := client.Lookup(context.Background(), "4CACB5", before, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(points) != 2 || !points[0].Timestamp.Before(points[1].Timestamp) {
		t.Fatalf("requests=%v points=%+v", requests, points)
	}
}

func TestLookupCombinesBothSidesOfUTCMidnight(t *testing.T) {
	before := time.Date(2026, 10, 12, 0, 2, 0, 0, time.UTC)
	client := NewClient("https://history.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/globe_history/2026/10/11/traces/23/trace_full_abc123.json":
			return jsonResponse(`{"icao":"abc123","timestamp":1791763140,"trace":[[0,59.0,18.0,10000]]}`), nil
		case "/data/traces/23/trace_recent_abc123.json":
			return jsonResponse(`{"icao":"abc123","timestamp":1791763200,"trace":[[60,59.1,18.1,10100]]}`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
	})}

	client.now = func() time.Time { return before }
	points, err := client.Lookup(context.Background(), "ABC123", before, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || !points[0].Timestamp.Before(points[1].Timestamp) {
		t.Fatalf("midnight history = %+v", points)
	}
}

func TestLookupRejectsInvalidICAO(t *testing.T) {
	client := NewClient("https://history.test")
	if _, err := client.Lookup(context.Background(), "not-hex", time.Now(), 5*time.Minute); err == nil {
		t.Fatal("invalid ICAO address was accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
