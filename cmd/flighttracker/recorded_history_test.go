package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sdr-flight-tracker/internal/tracking"
)

func TestRecordedHistoryEndpointListsDatesAndReplaysSavedAircraft(t *testing.T) {
	root := t.TempDir()
	recorder, err := tracking.NewRecorder(root)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 8, 19, 0, 0, 0, time.UTC)
	altitude := 12000
	speed := 245.5
	heading := 92.0
	firstPosition := &tracking.Position{Latitude: 59.2, Longitude: 18.2, Timestamp: start}
	secondPosition := &tracking.Position{Latitude: 59.21, Longitude: 18.25, Timestamp: start.Add(2 * time.Second)}
	for _, entry := range []tracking.Entry{
		{
			Event:            "adsb_frame",
			ReceivedAt:       start,
			ICAO:             "4D2410",
			Registration:     "9H-EXS",
			Callsign:         "EXS1",
			RawFrame:         "FRAME1",
			DownlinkFormat:   17,
			TypeCode:         11,
			AltitudeFeet:     &altitude,
			CPRReference:     "odd",
			StoredPosition:   firstPosition,
			PositionDecision: "accepted",
		},
		{
			Event:              "adsb_frame",
			ReceivedAt:         start.Add(2 * time.Second),
			ICAO:               "4D2410",
			Registration:       "9H-EXS",
			Callsign:           "EXS1",
			RawFrame:           "FRAME2",
			DownlinkFormat:     17,
			TypeCode:           19,
			AltitudeFeet:       &altitude,
			ReportedSpeedKnots: &speed,
			HeadingDegrees:     &heading,
			CPRReference:       "even",
			StoredPosition:     secondPosition,
			PositionDecision:   "accepted",
		},
		{
			Event:            "map_position_set",
			ReceivedAt:       start.Add(2500 * time.Millisecond),
			ICAO:             "4D2410",
			CPRReference:     "even",
			PositionDecision: "set_in_3d_world",
			WorldPosition: &tracking.WorldPosition{
				SetAt:        start.Add(2500 * time.Millisecond),
				Latitude:     secondPosition.Latitude,
				Longitude:    secondPosition.Longitude,
				AltitudeFeet: float64(altitude),
				CPRReference: "even",
			},
		},
	} {
		if err := recorder.Record(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Record(tracking.Entry{
		Event:          "adsb_frame",
		ReceivedAt:     start.Add(24 * time.Hour),
		ICAO:           "ABC123",
		RawFrame:       "FRAME3",
		DownlinkFormat: 17,
		TypeCode:       1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	endpoint := &recordedHistoryEndpoint{root: root}
	datesRequest := httptest.NewRequest(http.MethodGet, "/recorded-history", nil)
	datesResponse := httptest.NewRecorder()
	endpoint.ServeHTTP(datesResponse, datesRequest)
	if datesResponse.Code != http.StatusOK {
		t.Fatalf("date status = %d, body = %q", datesResponse.Code, datesResponse.Body.String())
	}
	var dates recordedHistoryDatesResponse
	if err := json.Unmarshal(datesResponse.Body.Bytes(), &dates); err != nil {
		t.Fatal(err)
	}
	if len(dates.Dates) != 2 || dates.Dates[0] != "2026-10-09" || dates.Dates[1] != "2026-10-08" {
		t.Fatalf("dates = %v", dates.Dates)
	}

	historyRequest := httptest.NewRequest(http.MethodGet, "/recorded-history?date=2026-10-08", nil)
	historyResponse := httptest.NewRecorder()
	endpoint.ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %q", historyResponse.Code, historyResponse.Body.String())
	}
	var history recordedHistoryResponse
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(history.Events))
	}
	if history.StartMilliseconds != start.UnixMilli() || history.EndMilliseconds != start.Add(2*time.Second).UnixMilli() {
		t.Fatalf("history bounds = %d..%d", history.StartMilliseconds, history.EndMilliseconds)
	}
	last := history.Events[1].Aircraft
	if last.ICAO != "4D2410" || last.Frames != 2 || last.Speed == nil || *last.Speed != speed || last.Latitude == nil || *last.Latitude != secondPosition.Latitude {
		t.Fatalf("unexpected replay aircraft: %+v", last)
	}
	if history.AircraftExpirySeconds != int64(aircraftExpiry/time.Second) {
		t.Fatalf("expiry = %d", history.AircraftExpirySeconds)
	}
}

func TestRecordedHistoryEndpointRejectsInvalidDate(t *testing.T) {
	endpoint := &recordedHistoryEndpoint{root: t.TempDir()}
	request := httptest.NewRequest(http.MethodGet, "/recorded-history?date=../../etc/passwd", nil)
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestRecordedHistoryReconstructsLegacyRawPosition(t *testing.T) {
	root := t.TempDir()
	recorder, err := tracking.NewRecorder(root)
	if err != nil {
		t.Fatal(err)
	}
	receivedAt := time.Date(2026, 10, 8, 19, 0, 7, 0, time.UTC)
	if err := recorder.Record(tracking.Entry{
		ReceivedAt:       receivedAt,
		ICAO:             "4D2410",
		Registration:     "9H-EXS",
		RawFrame:         "8D4D241058371394FB072EDB94B1",
		DownlinkFormat:   17,
		TypeCode:         11,
		PositionDecision: "not_decoded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := readRecordedHistoryAt(root, "2026-10-08", homeLatitude, homeLongitude)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	aircraft := events[0].Aircraft
	if !aircraft.PositionReady || aircraft.Latitude == nil || aircraft.Longitude == nil {
		t.Fatalf("legacy position was not reconstructed: %+v", aircraft)
	}
	if math.Abs(*aircraft.Latitude-59.37290954589844) > 1e-9 || math.Abs(*aircraft.Longitude-18.16827392578125) > 1e-9 {
		t.Fatalf("legacy position = %.9f, %.9f", *aircraft.Latitude, *aircraft.Longitude)
	}
}
