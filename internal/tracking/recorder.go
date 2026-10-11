package tracking

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Position is a decoded or retained aircraft position at a specific reception time.
type Position struct {
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Timestamp time.Time `json:"timestamp"`
}

// CPR records the raw compact-position fields carried by a position frame.
type CPR struct {
	Latitude  int  `json:"latitude"`
	Longitude int  `json:"longitude"`
	Odd       bool `json:"odd"`
}

// CPRSample records one half of the even/odd pair used for global decoding.
type CPRSample struct {
	Latitude   int       `json:"latitude"`
	Longitude  int       `json:"longitude"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// Cartesian is an Earth-centered, Earth-fixed position in meters used by Cesium.
type Cartesian struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// WorldPosition records a position that the browser actually inserted into Cesium.
type WorldPosition struct {
	SetAt            time.Time `json:"setAt"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	AltitudeFeet     float64   `json:"altitudeFeet"`
	Cartesian        Cartesian `json:"cartesianEcefMeters"`
	CPRReference     string    `json:"cprReference"`
	BrowserSessionID string    `json:"browserSessionId"`
}

// Entry contains enough input and validation state to reconstruct a plotted fix.
type Entry struct {
	Event              string         `json:"event"`
	ReceivedAt         time.Time      `json:"receivedAt"`
	ICAO               string         `json:"icao"`
	Registration       string         `json:"registration,omitempty"`
	Callsign           string         `json:"callsign,omitempty"`
	DepartureAirport   string         `json:"departureAirport,omitempty"`
	DestinationAirport string         `json:"destinationAirport,omitempty"`
	RawFrame           string         `json:"rawFrame"`
	DownlinkFormat     int            `json:"downlinkFormat"`
	TypeCode           int            `json:"typeCode"`
	AltitudeFeet       *int           `json:"altitudeFeet,omitempty"`
	ReportedSpeedKnots *float64       `json:"reportedSpeedKnots,omitempty"`
	HeadingDegrees     *float64       `json:"headingDegrees,omitempty"`
	CPR                *CPR           `json:"cpr,omitempty"`
	EvenCPR            *CPRSample     `json:"evenCpr,omitempty"`
	OddCPR             *CPRSample     `json:"oddCpr,omitempty"`
	CPRReference       string         `json:"cprReference,omitempty"`
	DecodedPosition    *Position      `json:"decodedPosition,omitempty"`
	PreviousPosition   *Position      `json:"previousPosition,omitempty"`
	StoredPosition     *Position      `json:"storedPosition,omitempty"`
	PositionDecision   string         `json:"positionDecision"`
	DecisionReason     string         `json:"decisionReason,omitempty"`
	TrackReset         bool           `json:"trackReset,omitempty"`
	ElapsedSeconds     *float64       `json:"elapsedSeconds,omitempty"`
	DistanceMeters     *float64       `json:"distanceMeters,omitempty"`
	ImpliedSpeedKnots  *float64       `json:"impliedSpeedKnots,omitempty"`
	AllowedSpeedKnots  *float64       `json:"allowedSpeedKnots,omitempty"`
	WorldPosition      *WorldPosition `json:"worldPosition,omitempty"`
}

// Recorder appends newline-delimited JSON records to one daily file per ICAO folder.
type Recorder struct {
	root  string
	mu    sync.Mutex
	files map[string]*os.File
}

func NewRecorder(root string) (*Recorder, error) {
	if root == "" {
		return &Recorder{}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Recorder{root: root, files: make(map[string]*os.File)}, nil
}

func (r *Recorder) Record(entry Entry) error {
	if r.root == "" {
		return nil
	}
	icao := strings.ToUpper(entry.ICAO)
	if !validICAO(icao) {
		return errors.New("invalid ICAO address")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	directory := filepath.Join(r.root, icao)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	date := entry.ReceivedAt.UTC().Format("2006-01-02")
	path := filepath.Join(directory, date+".jsonl")
	file := r.files[path]
	if file == nil {
		var err error
		file, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		r.files[path] = file
	}
	return json.NewEncoder(file).Encode(entry)
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result error
	for path, file := range r.files {
		if err := file.Close(); err != nil {
			result = errors.Join(result, err)
		}
		delete(r.files, path)
	}
	return result
}

func validICAO(icao string) bool {
	if len(icao) != 6 {
		return false
	}
	for _, character := range icao {
		if !strings.ContainsRune("0123456789ABCDEF", character) {
			return false
		}
	}
	return true
}
