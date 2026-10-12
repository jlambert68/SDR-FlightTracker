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
	"sdr-flight-tracker/internal/flighthistory"
	"sdr-flight-tracker/internal/tracking"
)

func TestUpdateCPRKnownPosition(t *testing.T) {
	states := make(map[string][2]*cprValue)
	even, err := adsb.DecodeLine("8D40621D58C382D690C8AC2863A7")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reference := cprDecodeReference{latitude: 52.0, longitude: -4.0, source: "receiver"}
	updateCPR(&even, states, now, reference)
	odd, err := adsb.DecodeLine("8D40621D58C386435CC412692AD6")
	if err != nil {
		t.Fatal(err)
	}
	updateCPR(&odd, states, now.Add(time.Second), reference)
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

func TestUpdateCPRStockholmLongitudeZone(t *testing.T) {
	states := make(map[string][2]*cprValue)
	even, err := adsb.DecodeLine("8D4AB565584303776900428E6A06")
	if err != nil {
		t.Fatal(err)
	}
	odd, err := adsb.DecodeLine("8D4AB5655841E6CF7AE6AF7D8FB9")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reference := cprDecodeReference{latitude: homeLatitude, longitude: homeLongitude, source: "receiver"}
	updateCPR(&even, states, now, reference)
	updateCPR(&odd, states, now.Add(2*time.Second), reference)
	if odd.Latitude == nil || odd.Longitude == nil {
		t.Fatal("position was not decoded")
	}
	if math.Abs(*odd.Latitude-59.20238947464247) > 1e-9 || math.Abs(*odd.Longitude-18.006875926050647) > 1e-9 {
		t.Fatalf("unexpected Stockholm position: %.9f, %.9f", *odd.Latitude, *odd.Longitude)
	}
}

func TestUpdateCPRDecodesSingleFramesLocally(t *testing.T) {
	states := make(map[string][2]*cprValue)
	even, err := adsb.DecodeLine("8D4D241058371394FB072EDB94B1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	receiver := cprDecodeReference{latitude: homeLatitude, longitude: homeLongitude, source: "receiver"}
	updateCPR(&even, states, now, receiver)
	if even.Latitude == nil || even.Longitude == nil {
		t.Fatal("single even frame was not locally decoded")
	}
	if math.Abs(*even.Latitude-59.37290954589844) > 1e-9 || math.Abs(*even.Longitude-18.16827392578125) > 1e-9 {
		t.Fatalf("unexpected local even position: %.9f, %.9f", *even.Latitude, *even.Longitude)
	}
	if even.PositionReference != "local_receiver_even" {
		t.Fatalf("position reference = %q, want local_receiver_even", even.PositionReference)
	}

	odd, err := adsb.DecodeLine("8D4D2410581F771DDAEE38817423")
	if err != nil {
		t.Fatal(err)
	}
	aircraft := cprDecodeReference{latitude: *even.Latitude, longitude: *even.Longitude, source: "aircraft"}
	updateCPR(&odd, states, now.Add(212*time.Second), aircraft)
	if odd.Latitude == nil || odd.Longitude == nil {
		t.Fatal("single odd frame was not locally decoded")
	}
	if math.Abs(*odd.Latitude-59.66940152443061) > 1e-9 || math.Abs(*odd.Longitude-18.18957098599138) > 1e-9 {
		t.Fatalf("unexpected local odd position: %.9f, %.9f", *odd.Latitude, *odd.Longitude)
	}
	if odd.PositionReference != "local_aircraft_odd" {
		t.Fatalf("position reference = %q, want local_aircraft_odd", odd.PositionReference)
	}
	reportedSpeed := 332.86633954186476
	even.SpeedKnots = &reportedSpeed
	evaluation := evaluatePosition(even, odd)
	if !evaluation.accepted || !evaluation.trackReset || evaluation.reason != "track_gap" {
		t.Fatalf("locally decoded frames did not form valid track segments: %+v", evaluation)
	}
}

func TestConsumeCapturedFlightPublishesPosition(t *testing.T) {
	recorder, err := tracking.NewRecorder("")
	if err != nil {
		t.Fatal(err)
	}

	flightMu.Lock()
	previousFlights := liveFlights
	liveFlights = make(map[string]adsb.Message)
	flightMu.Unlock()
	defer func() {
		flightMu.Lock()
		liveFlights = previousFlights
		flightMu.Unlock()
	}()

	frames := strings.Join([]string{
		"*8D4D241058371394FB072EDB94B1;",
		"*8D4D241099081929B86891358A20;",
		"*8D4D2410990813293840918FE568;",
		"*8D4D241099880F28D85C919C0C3A;",
		"*8D4D241099080C283844904214C8;",
		"*8D4D241099080C27F84C9021681F;",
		"*8D4D241099080C27F84C9021681F;",
		"*8D4D2410581F771DDAEE38817423;",
	}, "\n")
	opts := options{latitude: homeLatitude, longitude: homeLongitude}
	if err := consume(strings.NewReader(frames), opts, recorder); err != nil {
		t.Fatal(err)
	}

	flightMu.RLock()
	flight, ok := liveFlights["4D2410"]
	flightMu.RUnlock()
	if !ok {
		t.Fatal("captured aircraft was not published")
	}
	if flight.Latitude == nil || flight.Longitude == nil {
		t.Fatal("captured aircraft was published without a position")
	}
	if math.Abs(*flight.Latitude-59.37290954589844) > 1e-9 || math.Abs(*flight.Longitude-18.16827392578125) > 1e-9 {
		t.Fatalf("unexpected captured position: %.9f, %.9f", *flight.Latitude, *flight.Longitude)
	}
	webFlight := toWebAircraft(flight)
	if !webFlight.PositionReady || webFlight.PositionReference != "local_receiver_even" {
		t.Fatalf("captured aircraft is not ready for the sight alarm: %+v", webFlight)
	}
}

func TestCPRNLNearPoles(t *testing.T) {
	for latitude, want := range map[float64]int{84.5: 5, 86: 3, 87: 2, 88: 1} {
		if got := cprNL(latitude); got != want {
			t.Errorf("cprNL(%v) = %d, want %d", latitude, got, want)
		}
	}
}

func TestCPRNLStockholmLatitude(t *testing.T) {
	for latitude, want := range map[float64]int{58.84: 31, 58.85: 30, 59.2: 30, 59.96: 29} {
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
	reportedSpeed := 300.0
	previous.SpeedKnots = &reportedSpeed
	current := messageAt(59.57, 18.48, now.Add(trackGap))
	evaluation := evaluatePosition(previous, current)
	if !evaluation.accepted || !evaluation.trackReset || evaluation.reason != "track_gap" {
		t.Fatalf("unexpected track-gap evaluation: %+v", evaluation)
	}
}

func TestValidPositionRejectsImpossibleJumpAfterTrackGap(t *testing.T) {
	now := time.Now()
	previous := messageAt(59.32, 18.48, now)
	reportedSpeed := 230.0
	previous.SpeedKnots = &reportedSpeed
	current := messageAt(61.00, 22.00, now.Add(trackGap))
	evaluation := evaluatePosition(previous, current)
	if evaluation.accepted || evaluation.reason != "jump_exceeds_distance_limit" {
		t.Fatalf("unexpected track-gap jump evaluation: %+v", evaluation)
	}
}

func TestValidPositionRejectsCapturedSASDiscontinuity(t *testing.T) {
	positionTime := time.Date(2026, 10, 11, 14, 41, 13, 142798707, time.UTC)
	previous := messageAt(59.2884521484375, 17.588048135080644, positionTime)
	reportedSpeed := 231.24878378058557
	previous.SpeedKnots = &reportedSpeed
	current := messageAt(59.37570345603813, 18.093061775996766, positionTime.Add(101*time.Second+577844816*time.Nanosecond))
	evaluation := evaluatePosition(previous, current)
	if evaluation.accepted || evaluation.reason != "implied_speed_exceeds_limit" {
		t.Fatalf("captured false jump was not rejected: %+v", evaluation)
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

func TestSightBellEndpointValidatesAndRateLimitsRequests(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	rings := 0
	endpoint := &sightBellEndpoint{
		interval: 4 * time.Second,
		now:      func() time.Time { return now },
		ring:     func() { rings++ },
	}
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/sight-bell", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, request)
		return response
	}

	if response := post(`{"aircraft":["4D2410"]}`); response.Code != http.StatusNoContent || rings != 1 {
		t.Fatalf("first alert: status=%d rings=%d", response.Code, rings)
	}
	now = now.Add(3 * time.Second)
	if response := post(`{"aircraft":["4D2410"]}`); response.Code != http.StatusNoContent || rings != 1 {
		t.Fatalf("rate-limited alert: status=%d rings=%d", response.Code, rings)
	}
	now = now.Add(time.Second)
	if response := post(`{"aircraft":["4D2410","ABC123"]}`); response.Code != http.StatusNoContent || rings != 2 {
		t.Fatalf("second alert: status=%d rings=%d", response.Code, rings)
	}
	if response := post(`{"aircraft":["invalid"]}`); response.Code != http.StatusBadRequest || rings != 2 {
		t.Fatalf("invalid alert: status=%d rings=%d", response.Code, rings)
	}
	now = now.Add(4 * time.Second)
	if response := post(`{"test":true}`); response.Code != http.StatusNoContent || rings != 3 {
		t.Fatalf("test bell: status=%d rings=%d", response.Code, rings)
	}
	if response := post(`{"test":true,"aircraft":["4D2410"]}`); response.Code != http.StatusBadRequest || rings != 3 {
		t.Fatalf("ambiguous test bell: status=%d rings=%d", response.Code, rings)
	}

	request := httptest.NewRequest(http.MethodGet, "/sight-bell", nil)
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET alert: status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestFlightHistoryEndpointReturnsConfiguredWindow(t *testing.T) {
	client := flighthistory.NewClient("https://history.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.Path, "/globe_history/2026/10/08/traces/10/trace_full_4d2410.json"; got != want {
			t.Fatalf("history path = %q, want %q", got, want)
		}
		body := `{"icao":"4d2410","timestamp":1791486000,"trace":[[450,59.2,18.2,9400,200,90,0,0,null,"adsb_icao",9550]]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	endpoint := &flightHistoryEndpoint{client: client, lookback: 5 * time.Minute}
	request := httptest.NewRequest(http.MethodGet, "/flight-history?hex=4D2410&before=2026-10-08T19:10:00Z", nil)
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var history flightHistoryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.LookbackSeconds != 300 || len(history.Points) != 1 {
		t.Fatalf("unexpected history response: %+v", history)
	}
	if point := history.Points[0]; point.Latitude != 59.2 || point.Longitude != 18.2 || point.AltitudeFeet != 9400 {
		t.Fatalf("unexpected history point: %+v", point)
	}
}

func TestFlightHistoryEndpointValidatesRequest(t *testing.T) {
	endpoint := &flightHistoryEndpoint{lookback: 5 * time.Minute}
	for _, target := range []string{
		"/flight-history?hex=invalid&before=2026-10-08T19:10:00Z",
		"/flight-history?hex=4D2410&before=not-a-time",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
}

func TestServeAircraftImage(t *testing.T) {
	originalClient := aircraftImageHTTPClient
	originalURL := aircraftPhotoAPIBaseURL
	originalRegistrationURL := aircraftPhotoRegistrationAPIBaseURL
	aircraftPhotoAPIBaseURL = "https://api.planespotters.net/pub/photos/hex/"
	aircraftPhotoRegistrationAPIBaseURL = "https://api.planespotters.net/pub/photos/reg/"
	aircraftImageHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://api.planespotters.net/pub/photos/hex/abc123":
			if request.Header.Get("User-Agent") != aircraftPhotoUserAgent || request.Header.Get("Accept") != "application/json" {
				t.Fatalf("unexpected photo API headers: %v", request.Header)
			}
			body := `{"photos":[{"thumbnail_large":{"src":"https://t.plnspttrs.net/photo.jpg"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "https://t.plnspttrs.net/photo.jpg":
			if request.Header.Get("Referer") != "https://www.planespotters.net/" {
				t.Fatalf("unexpected image referer: %q", request.Header.Get("Referer"))
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("photo"))}, nil
		default:
			t.Fatalf("unexpected upstream request: %s", request.URL.String())
			return nil, nil
		}
	})}
	defer func() {
		aircraftPhotoAPIBaseURL = originalURL
		aircraftPhotoRegistrationAPIBaseURL = originalRegistrationURL
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

func TestServeAircraftImageFallsBackToRegistration(t *testing.T) {
	originalClient := aircraftImageHTTPClient
	originalURL := aircraftPhotoAPIBaseURL
	originalRegistrationURL := aircraftPhotoRegistrationAPIBaseURL
	aircraftPhotoAPIBaseURL = "https://photo.test/hex/"
	aircraftPhotoRegistrationAPIBaseURL = "https://photo.test/reg/"
	aircraftImageHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://photo.test/hex/4d2410":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"photos":[]}`))}, nil
		case "https://photo.test/reg/9H-EXS":
			body := `{"photos":[{"thumbnail":{"src":"https://t.plnspttrs.net/9h-exs.jpg"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "https://t.plnspttrs.net/9h-exs.jpg":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("registration photo"))}, nil
		default:
			t.Fatalf("unexpected upstream request: %s", request.URL.String())
			return nil, nil
		}
	})}
	defer func() {
		aircraftPhotoAPIBaseURL = originalURL
		aircraftPhotoRegistrationAPIBaseURL = originalRegistrationURL
		aircraftImageHTTPClient = originalClient
	}()

	request := httptest.NewRequest(http.MethodGet, "/aircraft-image?hex=4D2410&reg=9H-EXS", nil)
	response := httptest.NewRecorder()
	serveAircraftImage(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "registration photo" {
		t.Fatalf("unexpected image response: status=%d body=%q", response.Code, response.Body.String())
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
	body := `{"icao":"471EFB","setAt":"2026-10-08T19:02:00Z","latitude":59.077,"longitude":18.179,"altitudeFeet":20325,"cartesianEcefMeters":{"x":100,"y":200,"z":300},"cprReference":"local_aircraft_odd","browserSessionId":"browser-1"}`
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
	if entry.Event != "map_position_set" || entry.CPRReference != "local_aircraft_odd" || entry.WorldPosition == nil || entry.WorldPosition.Cartesian.Z != 300 {
		t.Fatalf("unexpected map tracking entry: %+v", entry)
	}
}

func TestMergeCarriesRouteOnlyForSameFlight(t *testing.T) {
	previous := adsb.Message{
		Callsign:           "KLM1023",
		DepartureAirport:   "Amsterdam Airport Schiphol (AMS/EHAM)",
		DestinationAirport: "London Heathrow Airport (LHR/EGLL)",
		Frames:             4,
	}
	continued := merge(previous, adsb.Message{})
	if continued.Callsign != previous.Callsign || continued.DepartureAirport != previous.DepartureAirport || continued.DestinationAirport != previous.DestinationAirport {
		t.Fatalf("route was not retained for the same flight: %+v", continued)
	}
	changed := merge(previous, adsb.Message{Callsign: "SAS123"})
	if changed.DepartureAirport != "" || changed.DestinationAirport != "" {
		t.Fatalf("old route was retained after callsign changed: %+v", changed)
	}
}

func messageAt(latitude, longitude float64, positionTime time.Time) adsb.Message {
	return adsb.Message{Latitude: &latitude, Longitude: &longitude, PositionTime: positionTime}
}
