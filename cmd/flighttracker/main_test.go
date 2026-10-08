package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sdr-flight-tracker/internal/adsb"
	"sdr-flight-tracker/internal/tracking"
)

func TestUpdateCPRKnownPosition(t *testing.T) {
	states := make(map[string][2]*cprValue)
	even, err := adsb.DecodeLine("8D40621D58C382D690C8AC2863A7")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	updateCPR(&even, states, now)
	odd, err := adsb.DecodeLine("8D40621D58C386435CC412692AD6")
	if err != nil {
		t.Fatal(err)
	}
	updateCPR(&odd, states, now.Add(time.Second))
	if odd.Latitude == nil || odd.Longitude == nil {
		t.Fatal("position was not decoded")
	}
	if odd.PositionReference != "odd" {
		t.Fatalf("position reference = %q, want odd", odd.PositionReference)
	}
	if math.Abs(*odd.Latitude-52.26578) > .001 || math.Abs(*odd.Longitude-3.93891) > .001 {
		t.Fatalf("unexpected position: %.5f, %.5f", *odd.Latitude, *odd.Longitude)
	}
}

func TestCPRNLNearPoles(t *testing.T) {
	for latitude, want := range map[float64]int{84.5: 3, 86: 2, 88: 1} {
		if got := cprNL(latitude); got != want {
			t.Errorf("cprNL(%v) = %d, want %d", latitude, got, want)
		}
	}
}

func TestValidPositionAcceptsPlausibleMovement(t *testing.T) {
	now := time.Now()
	previous := messageAt(59.32, 18.48, now)
	current := messageAt(59.36, 18.55, now.Add(30*time.Second))
	if !validPosition(previous, current) {
		t.Fatal("plausible position was rejected")
	}
}

func TestValidPositionRejectsImpossibleJump(t *testing.T) {
	now := time.Now()
	previous := messageAt(59.32, 18.48, now)
	current := messageAt(60.32, 20.48, now.Add(10*time.Second))
	if validPosition(previous, current) {
		t.Fatal("impossible position jump was accepted")
	}
}

func TestValidPositionStartsAgainAfterTrackGap(t *testing.T) {
	now := time.Now()
	previous := messageAt(59.32, 18.48, now)
	current := messageAt(61.00, 22.00, now.Add(trackGap))
	if !validPosition(previous, current) {
		t.Fatal("first position after a track gap was rejected")
	}
}

func TestValidPositionUsesReportedSpeed(t *testing.T) {
	now := time.Now()
	reportedSpeed := 328.0
	previous := messageAt(59.32, 18.48, now)
	previous.SpeedKnots = &reportedSpeed
	current := messageAt(59.68, 18.48, now.Add(100*time.Second))
	evaluation := evaluatePosition(previous, current)
	if evaluation.accepted || evaluation.reason != "implied_speed_exceeds_limit" {
		t.Fatalf("unexpected evaluation: %+v", evaluation)
	}
	if evaluation.allowedSpeedKnots != 674 {
		t.Fatalf("allowed speed = %.0f, want 674", evaluation.allowedSpeedKnots)
	}
}

func TestExpireFlights(t *testing.T) {
	now := time.Now()
	flights := map[string]adsb.Message{
		"CURRENT": {LastSeen: now.Add(-time.Minute)},
		"STALE":   {LastSeen: now.Add(-aircraftExpiry - time.Second)},
	}
	expireFlights(flights, now)
	if _, ok := flights["CURRENT"]; !ok {
		t.Fatal("current aircraft was expired")
	}
	if _, ok := flights["STALE"]; ok {
		t.Fatal("stale aircraft was retained")
	}
}

func TestExpireCPR(t *testing.T) {
	now := time.Now()
	states := map[string][2]*cprValue{
		"CURRENT": {{received: now.Add(-time.Second)}, nil},
		"STALE":   {{received: now.Add(-cprExpiry - time.Second)}, nil},
	}
	expireCPR(states, now)
	if _, ok := states["CURRENT"]; !ok {
		t.Fatal("current CPR state was expired")
	}
	if _, ok := states["STALE"]; ok {
		t.Fatal("stale CPR state was retained")
	}
}

func TestServeAircraftImage(t *testing.T) {
	originalClient := aircraftImageHTTPClient
	originalURL := aircraftPhotoAPIBaseURL
	aircraftPhotoAPIBaseURL = "https://api.planespotters.net/pub/photos/hex/"
	aircraftImageHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://api.planespotters.net/pub/photos/hex/abc123":
			body := `{"photos":[{"thumbnail":{"src":"https://cdn.planespotters.net/photo.jpg"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "https://cdn.planespotters.net/photo.jpg":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("photo"))}, nil
		default:
			t.Fatalf("unexpected upstream request: %s", request.URL.String())
			return nil, nil
		}
	})}
	defer func() {
		aircraftPhotoAPIBaseURL = originalURL
		aircraftImageHTTPClient = originalClient
	}()

	request := httptest.NewRequest(http.MethodGet, "/aircraft-image?hex=ABC123", nil)
	response := httptest.NewRecorder()
	serveAircraftImage(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "photo" {
		t.Fatalf("unexpected image response: status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("unexpected cache header: %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestServeAircraftImageRejectsInvalidAddress(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/aircraft-image?hex=not-a-hex-code", nil)
	response := httptest.NewRecorder()
	serveAircraftImage(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestRecordMapPosition(t *testing.T) {
	root := t.TempDir()
	recorder, err := tracking.NewRecorder(root)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"icao":"471EFB","setAt":"2026-10-08T19:02:00Z","latitude":59.077,"longitude":18.179,"altitudeFeet":20325,"cartesianEcefMeters":{"x":100,"y":200,"z":300},"cprReference":"even","browserSessionId":"browser-1"}`
	request := httptest.NewRequest(http.MethodPost, "/tracking/map-position", strings.NewReader(body))
	response := httptest.NewRecorder()
	recordMapPosition(response, request, recorder)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "471EFB", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("tracking files = %v, error = %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var entry tracking.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Event != "map_position_set" || entry.CPRReference != "even" || entry.WorldPosition == nil || entry.WorldPosition.Cartesian.Z != 300 {
		t.Fatalf("unexpected map tracking entry: %+v", entry)
	}
}

func messageAt(latitude, longitude float64, positionTime time.Time) adsb.Message {
	return adsb.Message{Latitude: &latitude, Longitude: &longitude, PositionTime: positionTime}
}
