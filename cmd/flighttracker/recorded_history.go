package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sdr-flight-tracker/internal/adsb"
	"sdr-flight-tracker/internal/tracking"
)

type recordedHistoryEndpoint struct {
	root      string
	latitude  float64
	longitude float64
}

type recordedHistoryDatesResponse struct {
	Dates []string `json:"dates"`
}

type recordedHistoryResponse struct {
	Date                  string                 `json:"date"`
	StartMilliseconds     int64                  `json:"startMilliseconds"`
	EndMilliseconds       int64                  `json:"endMilliseconds"`
	AircraftExpirySeconds int64                  `json:"aircraftExpirySeconds"`
	Events                []recordedHistoryEvent `json:"events"`
}

type recordedHistoryEvent struct {
	Timestamp    string      `json:"timestamp"`
	Milliseconds int64       `json:"milliseconds"`
	Aircraft     webAircraft `json:"aircraft"`
}

type recordedEntry struct {
	entry    tracking.Entry
	sequence int
}

type recordedAircraftState struct {
	aircraft     webAircraft
	decoded      adsb.Message
	decodedReady bool
	lastSeen     time.Time
}

func (endpoint *recordedHistoryEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		dates, err := recordedHistoryDates(endpoint.root)
		if err != nil {
			log.Printf("list recorded history dates: %v", err)
			http.Error(w, "recorded history unavailable", http.StatusInternalServerError)
			return
		}
		writeRecordedHistoryJSON(w, recordedHistoryDatesResponse{Dates: dates})
		return
	}
	if !validRecordedHistoryDate(date) {
		http.Error(w, "invalid history date", http.StatusBadRequest)
		return
	}

	events, err := readRecordedHistoryAt(endpoint.root, date, endpoint.latitude, endpoint.longitude)
	if err != nil {
		log.Printf("read recorded history date=%s: %v", date, err)
		http.Error(w, "recorded history unavailable", http.StatusInternalServerError)
		return
	}
	response := recordedHistoryResponse{
		Date:                  date,
		AircraftExpirySeconds: int64(aircraftExpiry / time.Second),
		Events:                events,
	}
	if len(events) > 0 {
		response.StartMilliseconds = events[0].Milliseconds
		response.EndMilliseconds = events[len(events)-1].Milliseconds
	}
	writeRecordedHistoryJSON(w, response)
}

func writeRecordedHistoryJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write recorded history response: %v", err)
	}
}

func recordedHistoryDates(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return []string{}, nil
	}
	directories, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	dates := make(map[string]struct{})
	for _, directory := range directories {
		if !directory.IsDir() || !validRecordedHistoryICAO(directory.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, directory.Name()))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
				continue
			}
			date := strings.TrimSuffix(file.Name(), ".jsonl")
			if validRecordedHistoryDate(date) {
				dates[date] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(dates))
	for date := range dates {
		result = append(result, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(result)))
	return result, nil
}

func readRecordedHistoryAt(root, date string, receiverLatitude, receiverLongitude float64) ([]recordedHistoryEvent, error) {
	if strings.TrimSpace(root) == "" {
		return []recordedHistoryEvent{}, nil
	}
	directories, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []recordedHistoryEvent{}, nil
	}
	if err != nil {
		return nil, err
	}

	entries := make([]recordedEntry, 0)
	sequence := 0
	for _, directory := range directories {
		icao := strings.ToUpper(directory.Name())
		if !directory.IsDir() || !validRecordedHistoryICAO(icao) {
			continue
		}
		path := filepath.Join(root, directory.Name(), date+".jsonl")
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			var entry tracking.Entry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				log.Printf("skip invalid recorded history path=%s line=%d: %v", path, line, err)
				continue
			}
			if entry.ReceivedAt.IsZero() {
				continue
			}
			if entry.ICAO == "" {
				entry.ICAO = icao
			}
			entry.ICAO = strings.ToUpper(entry.ICAO)
			if !validRecordedHistoryICAO(entry.ICAO) {
				continue
			}
			entries = append(entries, recordedEntry{entry: entry, sequence: sequence})
			sequence++
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil || closeErr != nil {
			return nil, errors.Join(scanErr, closeErr)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].entry.ReceivedAt.Equal(entries[j].entry.ReceivedAt) {
			return entries[i].sequence < entries[j].sequence
		}
		return entries[i].entry.ReceivedAt.Before(entries[j].entry.ReceivedAt)
	})

	states := make(map[string]recordedAircraftState)
	cprStates := make(map[string][2]*cprValue)
	events := make([]recordedHistoryEvent, 0, len(entries))
	for _, recorded := range entries {
		entry := recorded.entry
		state := states[entry.ICAO]
		if state.aircraft.ICAO == "" || (!state.lastSeen.IsZero() && entry.ReceivedAt.Sub(state.lastSeen) > aircraftExpiry) {
			state = recordedAircraftState{aircraft: webAircraft{ICAO: entry.ICAO}}
			delete(cprStates, entry.ICAO)
		}
		fallback := replayRecordedFrame(&state, entry, cprStates, receiverLatitude, receiverLongitude)
		emit := applyRecordedEntry(&state.aircraft, entry)
		applyRecordedFallback(&state.aircraft, entry, fallback)
		state.lastSeen = entry.ReceivedAt
		states[entry.ICAO] = state
		if !emit {
			continue
		}
		events = append(events, recordedHistoryEvent{
			Timestamp:    entry.ReceivedAt.UTC().Format(time.RFC3339Nano),
			Milliseconds: entry.ReceivedAt.UnixMilli(),
			Aircraft:     cloneWebAircraft(state.aircraft),
		})
	}
	return events, nil
}

func replayRecordedFrame(state *recordedAircraftState, entry tracking.Entry, cprStates map[string][2]*cprValue, receiverLatitude, receiverLongitude float64) *adsb.Message {
	event := entry.Event
	if event == "" {
		event = "adsb_frame"
	}
	if event != "adsb_frame" || entry.RawFrame == "" {
		return nil
	}
	message, err := adsb.DecodeLine(entry.RawFrame)
	if err != nil || message.ICAO != entry.ICAO {
		return nil
	}
	message.LastSeen = entry.ReceivedAt
	if message.TypeCode >= 9 && message.TypeCode <= 18 {
		reference := cprDecodeReference{latitude: receiverLatitude, longitude: receiverLongitude, source: "receiver"}
		if state.decodedReady && state.decoded.Latitude != nil && state.decoded.Longitude != nil {
			reference = cprDecodeReference{latitude: *state.decoded.Latitude, longitude: *state.decoded.Longitude, source: "aircraft"}
		}
		updateCPR(&message, cprStates, entry.ReceivedAt, reference)
	}
	if state.decodedReady {
		if message.Latitude != nil && !evaluatePosition(state.decoded, message).accepted {
			message.Latitude, message.Longitude = nil, nil
			message.PositionTime = time.Time{}
			message.PositionReference = ""
		}
		message = merge(state.decoded, message)
	}
	if entry.StoredPosition != nil {
		latitude, longitude := entry.StoredPosition.Latitude, entry.StoredPosition.Longitude
		message.Latitude, message.Longitude = &latitude, &longitude
		message.PositionTime = entry.StoredPosition.Timestamp
		message.PositionReference = entry.CPRReference
	}
	if entry.AltitudeFeet != nil {
		message.AltitudeFeet = intPointer(*entry.AltitudeFeet)
	}
	if entry.ReportedSpeedKnots != nil {
		message.SpeedKnots = floatPointer(*entry.ReportedSpeedKnots)
	}
	if entry.HeadingDegrees != nil {
		message.HeadingDeg = floatPointer(*entry.HeadingDegrees)
	}
	if entry.Callsign != "" {
		message.Callsign = entry.Callsign
	}
	message.DepartureAirport = entry.DepartureAirport
	message.DestinationAirport = entry.DestinationAirport
	state.decoded, state.decodedReady = message, true
	return &state.decoded
}

func applyRecordedFallback(aircraft *webAircraft, entry tracking.Entry, message *adsb.Message) {
	if message == nil {
		return
	}
	if aircraft.Callsign == "" {
		aircraft.Callsign = message.Callsign
	}
	if aircraft.Altitude == nil && message.AltitudeFeet != nil {
		aircraft.Altitude = intPointer(*message.AltitudeFeet)
	}
	if aircraft.Speed == nil && message.SpeedKnots != nil {
		aircraft.Speed = floatPointer(*message.SpeedKnots)
	}
	if aircraft.Heading == nil && message.HeadingDeg != nil {
		aircraft.Heading = floatPointer(*message.HeadingDeg)
	}
	if message.VerticalFPM != nil {
		aircraft.Vertical = intPointer(*message.VerticalFPM)
	}
	if entry.StoredPosition == nil && message.Latitude != nil && message.Longitude != nil && !message.PositionTime.IsZero() {
		setRecordedPosition(aircraft, *message.Latitude, *message.Longitude, message.PositionTime, message.PositionReference)
	}
}

func applyRecordedEntry(aircraft *webAircraft, entry tracking.Entry) bool {
	event := entry.Event
	if event == "" {
		event = "adsb_frame"
	}
	switch event {
	case "adsb_frame":
		aircraft.Frames++
		aircraft.Raw = entry.RawFrame
		aircraft.Downlink = entry.DownlinkFormat
		aircraft.MessageType = entry.TypeCode
		setRecordedString(&aircraft.Registration, entry.Registration)
		setRecordedString(&aircraft.Callsign, entry.Callsign)
		setRecordedString(&aircraft.DepartureAirport, entry.DepartureAirport)
		setRecordedString(&aircraft.DestinationAirport, entry.DestinationAirport)
		if entry.AltitudeFeet != nil {
			aircraft.Altitude = intPointer(*entry.AltitudeFeet)
		}
		if entry.ReportedSpeedKnots != nil {
			aircraft.Speed = floatPointer(*entry.ReportedSpeedKnots)
		}
		if entry.HeadingDegrees != nil {
			aircraft.Heading = floatPointer(*entry.HeadingDegrees)
		}
		if entry.StoredPosition != nil {
			setRecordedPosition(aircraft, entry.StoredPosition.Latitude, entry.StoredPosition.Longitude, entry.StoredPosition.Timestamp, entry.CPRReference)
		}
		return true
	case "map_position_set":
		if entry.WorldPosition == nil {
			return false
		}
		position := entry.WorldPosition
		if aircraft.PositionReady && aircraft.Latitude != nil && aircraft.Longitude != nil &&
			*aircraft.Latitude == position.Latitude && *aircraft.Longitude == position.Longitude &&
			aircraft.Altitude != nil && float64(*aircraft.Altitude) == position.AltitudeFeet {
			return false
		}
		altitude := int(position.AltitudeFeet)
		aircraft.Altitude = intPointer(altitude)
		setRecordedPosition(aircraft, position.Latitude, position.Longitude, position.SetAt, position.CPRReference)
		return true
	default:
		return false
	}
}

func setRecordedPosition(aircraft *webAircraft, latitude, longitude float64, timestamp time.Time, reference string) {
	aircraft.PositionReady = true
	aircraft.Latitude = floatPointer(latitude)
	aircraft.Longitude = floatPointer(longitude)
	if !timestamp.IsZero() {
		aircraft.PositionTime = timestamp.UTC().Format(time.RFC3339Nano)
	}
	if reference == "" {
		reference = "historical_log"
	}
	aircraft.PositionReference = reference
}

func setRecordedString(target *string, value string) {
	if value != "" {
		*target = value
	}
}

func cloneWebAircraft(aircraft webAircraft) webAircraft {
	result := aircraft
	if aircraft.Altitude != nil {
		result.Altitude = intPointer(*aircraft.Altitude)
	}
	if aircraft.Speed != nil {
		result.Speed = floatPointer(*aircraft.Speed)
	}
	if aircraft.Heading != nil {
		result.Heading = floatPointer(*aircraft.Heading)
	}
	if aircraft.Vertical != nil {
		result.Vertical = intPointer(*aircraft.Vertical)
	}
	if aircraft.Latitude != nil {
		result.Latitude = floatPointer(*aircraft.Latitude)
	}
	if aircraft.Longitude != nil {
		result.Longitude = floatPointer(*aircraft.Longitude)
	}
	return result
}

func intPointer(value int) *int { return &value }

func validRecordedHistoryDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validRecordedHistoryICAO(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range strings.ToUpper(value) {
		if !strings.ContainsRune("0123456789ABCDEF", character) {
			return false
		}
	}
	return true
}
