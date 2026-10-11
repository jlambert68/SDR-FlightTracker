package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sdr-flight-tracker/internal/adsb"
	"sdr-flight-tracker/internal/aircraft"
	"sdr-flight-tracker/internal/flightroute"
	"sdr-flight-tracker/internal/location"
	"sdr-flight-tracker/internal/tracking"
)

const bellSound = "/usr/share/sounds/freedesktop/stereo/bell.oga"
const homeAddress = "Tottkällevägen 4A, Värmdö"
const homeLatitude = 59.323316
const homeLongitude = 18.480349

const aircraftExpiry = 5 * time.Minute
const cprExpiry = 30 * time.Second
const trackGap = 2 * time.Minute
const sightBellRateLimit = 4 * time.Second
const bellPlayerTimeout = time.Second
const maximumPositionJumpMeters = 100_000.0
const maximumGroundSpeedKnots = 1_500.0
const unknownGroundSpeedLimitKnots = 600.0
const positionErrorAllowanceMeters = 250.0
const localCPRReferenceRangeMeters = 180 * 1_852.0
const aircraftPhotoUserAgent = "SDR-FlightTracker/1.0 (+https://github.com/jlambert68/SDR-FlightTracker)"

var soundLookup sync.Once
var soundFailureLog sync.Once
var soundPlayers []bellCommand
var bellMu sync.Mutex
var bellPlaying bool
var aircraftPhotoAPIBaseURL = "https://api.planespotters.net/pub/photos/hex/"
var aircraftPhotoRegistrationAPIBaseURL = "https://api.planespotters.net/pub/photos/reg/"
var aircraftImageHTTPClient = http.DefaultClient
var flightMu sync.RWMutex
var liveFlights = map[string]adsb.Message{}
var receiverPosition = struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}{}

type options struct {
	device      string
	gain        string
	ppm         int
	biasTee     bool
	mute        bool
	metadata    bool
	metadataURL string
	routes      bool
	routeURL    string
	input       string
	latitude    float64
	longitude   float64
	trackingDir string
}

type bellCommand struct {
	path string
	args []string
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	var opts options
	flag.StringVar(&opts.device, "device", "0", "RTL-SDR device index or serial number")
	flag.StringVar(&opts.gain, "gain", "auto", "tuner gain in dB, or auto")
	flag.IntVar(&opts.ppm, "ppm", 0, "frequency correction in PPM")
	flag.BoolVar(&opts.biasTee, "bias-tee", false, "enable the dongle bias tee")
	flag.BoolVar(&opts.mute, "mute", false, "disable startup and Home sight-volume bells")
	flag.BoolVar(&opts.metadata, "metadata", true, "look up readable aircraft information online")
	flag.StringVar(&opts.metadataURL, "metadata-url", "https://hexdb.io/api/v1/aircraft", "aircraft metadata service URL")
	flag.BoolVar(&opts.routes, "routes", true, "look up departure and destination airports online")
	flag.StringVar(&opts.routeURL, "route-url", "https://api.adsbdb.com/v0/callsign", "flight route service URL")
	flag.StringVar(&opts.input, "input", "", "read rtl_adsb text from a file, or - for stdin")
	flag.Float64Var(&opts.latitude, "lat", homeLatitude, "receiver latitude")
	flag.Float64Var(&opts.longitude, "lon", homeLongitude, "receiver longitude")
	flag.StringVar(&opts.trackingDir, "tracking-dir", "tracking-data", "directory for per-aircraft JSON tracking logs; empty disables logging")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupBeep(opts.mute)
	if opts.latitude == 0 && opts.longitude == 0 {
		lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		p, err := location.Lookup(lctx)
		cancel()
		if err != nil {
			log.Printf("receiver location lookup failed: %v", err)
		} else {
			opts.latitude, opts.longitude = p.Latitude, p.Longitude
			log.Printf("receiver position latitude=%.5f longitude=%.5f (%s, %s)", p.Latitude, p.Longitude, p.City, p.Country)
		}
	} else {
		locationName := "configured"
		if opts.latitude == homeLatitude && opts.longitude == homeLongitude {
			locationName = homeAddress
		}
		log.Printf("receiver position latitude=%.5f longitude=%.5f (%s)", opts.latitude, opts.longitude, locationName)
	}
	receiverPosition.Latitude, receiverPosition.Longitude = opts.latitude, opts.longitude
	trackRecorder, err := tracking.NewRecorder(opts.trackingDir)
	if err != nil {
		log.Fatalf("create tracking recorder: %v", err)
	}
	if opts.trackingDir != "" {
		log.Printf("tracking data directory=%s", opts.trackingDir)
	}
	defer func() {
		if err := trackRecorder.Close(); err != nil {
			log.Printf("close tracking recorder: %v", err)
		}
	}()
	go serveWeb(trackRecorder, opts.mute)

	if err := run(ctx, opts, trackRecorder); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("flight tracker: %v", err)
	}
}

func run(ctx context.Context, opts options, trackRecorder *tracking.Recorder) error {
	if opts.input != "" {
		if opts.input == "-" {
			return consume(os.Stdin, opts, trackRecorder)
		}
		f, err := os.Open(opts.input)
		if err != nil {
			return err
		}
		defer f.Close()
		return consume(f, opts, trackRecorder)
	}

	path, err := exec.LookPath("rtl_adsb")
	if err != nil {
		return fmt.Errorf("RTL-SDR decoder rtl_adsb is not installed or not in PATH (see README): %w", err)
	}

	args := []string{"-d", opts.device, "-p", strconv.Itoa(opts.ppm)}
	if opts.gain != "auto" {
		if _, err := strconv.ParseFloat(opts.gain, 64); err != nil {
			return fmt.Errorf("invalid gain %q: use a dB value or auto", opts.gain)
		}
		args = append(args, "-g", opts.gain)
	}
	if opts.biasTee {
		args = append(args, "-T")
	}

	cmd := exec.CommandContext(ctx, path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	log.Printf("listening for ADS-B on 1090 MHz with RTL-SDR device %s", opts.device)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start rtl_adsb; check USB permissions and that the dongle is connected: %w", err)
	}
	scanErr := consume(stdout, opts, trackRecorder)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("could not read rtl_adsb output: %w", scanErr)
	}
	if waitErr != nil {
		return fmt.Errorf("rtl_adsb stopped; the dongle may be unavailable or claimed by another driver: %w", waitErr)
	}
	return waitErr
}

func consume(r io.Reader, opts options, trackRecorder *tracking.Recorder) error {
	cpr := make(map[string][2]*cprValue)
	metadataClient := aircraft.NewClient(opts.metadataURL)
	routeClient := flightroute.NewClient(opts.routeURL)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		message, err := adsb.DecodeLine(raw)
		if err != nil {
			continue
		}
		now := time.Now()
		message.LastSeen = now
		expireCPR(cpr, now)
		if opts.metadata {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var lookupErr error
			message.Metadata, lookupErr = metadataClient.Lookup(ctx, message.ICAO)
			if lookupErr != nil {
				log.Printf("metadata lookup failed for aircraft=%s endpoint=%s: %v", message.ICAO, opts.metadataURL, lookupErr)
			}
			cancel()
		}
		if opts.routes && message.Callsign != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			route, lookupErr := routeClient.Lookup(ctx, message.Callsign)
			if lookupErr != nil {
				log.Printf("route lookup failed for aircraft=%s callsign=%s endpoint=%s: %v", message.ICAO, message.Callsign, opts.routeURL, lookupErr)
			} else {
				message.DepartureAirport = route.Origin.String()
				message.DestinationAirport = route.Destination.String()
			}
			cancel()
		}
		if message.TypeCode >= 9 && message.TypeCode <= 18 {
			reference := cprDecodeReference{latitude: opts.latitude, longitude: opts.longitude, source: "receiver"}
			flightMu.RLock()
			previous := liveFlights[message.ICAO]
			if previous.Latitude != nil && previous.Longitude != nil {
				reference = cprDecodeReference{latitude: *previous.Latitude, longitude: *previous.Longitude, source: "aircraft"}
			}
			flightMu.RUnlock()
			updateCPR(&message, cpr, now, reference)
		}
		flightMu.Lock()
		previous, exists := liveFlights[message.ICAO]
		decodedMessage := message
		evaluation := positionEvaluation{decision: "not_decoded"}
		if message.Latitude != nil {
			evaluation = evaluatePosition(previous, message)
		}
		if exists {
			if message.Latitude != nil && !evaluation.accepted {
				log.Printf("position rejected for aircraft=%s latitude=%.5f longitude=%.5f reason=%s distance=%.0fm implied_speed=%.0fkt allowed_speed=%.0fkt", message.ICAO, *message.Latitude, *message.Longitude, evaluation.reason, evaluation.distanceMeters, evaluation.impliedSpeedKnots, evaluation.allowedSpeedKnots)
				message.Latitude, message.Longitude = nil, nil
				message.PositionTime = time.Time{}
			}
			message = merge(previous, message)
		}
		liveFlights[message.ICAO] = message
		expireFlights(liveFlights, now)
		flightMu.Unlock()
		if err := trackRecorder.Record(makeTrackingEntry(now, decodedMessage, previous, exists, message, evaluation, cpr[message.ICAO])); err != nil {
			log.Printf("write tracking data for aircraft=%s: %v", message.ICAO, err)
		}
		log.Printf("%s", message)
	}
	return scanner.Err()
}

func serveWeb(trackRecorder *tracking.Recorder, mute bool) {
	http.HandleFunc("/aircraft-image", serveAircraftImage)
	http.Handle("/sight-bell", &sightBellEndpoint{
		interval: sightBellRateLimit,
		now:      time.Now,
		ring:     func() { beep(mute) },
	})
	http.HandleFunc("/tracking/map-position", func(w http.ResponseWriter, r *http.Request) {
		recordMapPosition(w, r, trackRecorder)
	})
	webFiles := http.FileServer(http.Dir("web"))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		webFiles.ServeHTTP(w, r)
	})
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", 500)
			return
		}
		for {
			flightMu.Lock()
			expireFlights(liveFlights, time.Now())
			aircraftList := make([]webAircraft, 0, len(liveFlights))
			for _, flight := range liveFlights {
				aircraftList = append(aircraftList, toWebAircraft(flight))
			}
			payload := map[string]any{"receiver": receiverPosition, "aircraft": aircraftList, "soundMuted": mute}
			data, _ := json.Marshal(payload)
			flightMu.Unlock()
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
	log.Printf("web map available at http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Printf("web server stopped: %v", err)
	}
}

type sightBellEndpoint struct {
	mu       sync.Mutex
	lastRing time.Time
	interval time.Duration
	now      func() time.Time
	ring     func()
}

type sightBellRequest struct {
	Aircraft []string `json:"aircraft"`
}

func (endpoint *sightBellEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var alert sightBellRequest
	if err := json.NewDecoder(r.Body).Decode(&alert); err != nil || !validSightBellAircraft(alert.Aircraft) {
		http.Error(w, "invalid sight bell request", http.StatusBadRequest)
		return
	}

	now := endpoint.now()
	endpoint.mu.Lock()
	if !endpoint.lastRing.IsZero() && now.Sub(endpoint.lastRing) < endpoint.interval {
		endpoint.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	endpoint.lastRing = now
	endpoint.mu.Unlock()
	endpoint.ring()
	w.WriteHeader(http.StatusNoContent)
}

func validSightBellAircraft(aircraft []string) bool {
	if len(aircraft) == 0 || len(aircraft) > 256 {
		return false
	}
	for _, icao := range aircraft {
		if len(icao) != 6 {
			return false
		}
		if _, err := hex.DecodeString(icao); err != nil {
			return false
		}
	}
	return true
}

type mapPositionRequest struct {
	ICAO             string             `json:"icao"`
	SetAt            time.Time          `json:"setAt"`
	Latitude         float64            `json:"latitude"`
	Longitude        float64            `json:"longitude"`
	AltitudeFeet     float64            `json:"altitudeFeet"`
	Cartesian        tracking.Cartesian `json:"cartesianEcefMeters"`
	CPRReference     string             `json:"cprReference"`
	BrowserSessionID string             `json:"browserSessionId"`
}

func recordMapPosition(w http.ResponseWriter, r *http.Request, recorder *tracking.Recorder) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var position mapPositionRequest
	if err := json.NewDecoder(r.Body).Decode(&position); err != nil {
		http.Error(w, "invalid map position", http.StatusBadRequest)
		return
	}
	position.ICAO = strings.ToUpper(strings.TrimSpace(position.ICAO))
	if len(position.ICAO) != 6 || position.SetAt.IsZero() || position.Latitude < -90 || position.Latitude > 90 || position.Longitude < -180 || position.Longitude > 180 || !finite(position.AltitudeFeet, position.Cartesian.X, position.Cartesian.Y, position.Cartesian.Z) || !validCPRReference(position.CPRReference) {
		http.Error(w, "invalid map position", http.StatusBadRequest)
		return
	}
	entry := tracking.Entry{
		Event:            "map_position_set",
		ReceivedAt:       time.Now(),
		ICAO:             position.ICAO,
		PositionDecision: "set_in_3d_world",
		CPRReference:     position.CPRReference,
		WorldPosition: &tracking.WorldPosition{
			SetAt:            position.SetAt,
			Latitude:         position.Latitude,
			Longitude:        position.Longitude,
			AltitudeFeet:     position.AltitudeFeet,
			Cartesian:        position.Cartesian,
			CPRReference:     position.CPRReference,
			BrowserSessionID: position.BrowserSessionID,
		},
	}
	if err := recorder.Record(entry); err != nil {
		log.Printf("write 3D map position for aircraft=%s: %v", position.ICAO, err)
		http.Error(w, "could not record map position", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validCPRReference(reference string) bool {
	switch reference {
	case "even", "odd", "local_receiver_even", "local_receiver_odd", "local_aircraft_even", "local_aircraft_odd":
		return true
	default:
		return false
	}
}

func finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func serveAircraftImage(w http.ResponseWriter, r *http.Request) {
	icao := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hex")))
	if len(icao) != 6 {
		http.Error(w, "invalid aircraft address", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(icao); err != nil {
		http.Error(w, "invalid aircraft address", http.StatusBadRequest)
		return
	}
	registration := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("reg")))
	if !validAircraftRegistration(registration) {
		registration = ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	endpoints := []string{aircraftPhotoAPIBaseURL + icao}
	if registration != "" {
		endpoints = append(endpoints, aircraftPhotoRegistrationAPIBaseURL+url.PathEscape(registration))
	}
	var imageURL string
	var lookupErr error
	for _, endpoint := range endpoints {
		imageURL, lookupErr = lookupAircraftPhoto(ctx, endpoint)
		if imageURL != "" {
			break
		}
		if lookupErr != nil {
			log.Printf("aircraft photo lookup failed for aircraft=%s endpoint=%s: %v", icao, endpoint, lookupErr)
		}
	}
	if imageURL == "" {
		status := http.StatusNotFound
		if lookupErr != nil {
			status = http.StatusBadGateway
		}
		http.Error(w, "aircraft image unavailable", status)
		return
	}
	parsedURL, err := url.Parse(imageURL)
	imageHost := ""
	if parsedURL != nil {
		imageHost = parsedURL.Hostname()
	}
	if err != nil || parsedURL == nil || parsedURL.Scheme != "https" || !isPlaneSpottersImageHost(imageHost) {
		log.Printf("aircraft photo API returned an unsupported image URL for aircraft=%s host=%q", icao, imageHost)
		http.Error(w, "aircraft image unavailable", http.StatusNotFound)
		return
	}
	imageRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		http.Error(w, "aircraft image unavailable", http.StatusBadGateway)
		return
	}
	imageRequest.Header.Set("User-Agent", aircraftPhotoUserAgent)
	imageRequest.Header.Set("Referer", "https://www.planespotters.net/")
	imageResponse, err := aircraftImageHTTPClient.Do(imageRequest)
	if err != nil {
		http.Error(w, "aircraft image unavailable", http.StatusBadGateway)
		return
	}
	defer imageResponse.Body.Close()
	if imageResponse.StatusCode != http.StatusOK || !strings.HasPrefix(imageResponse.Header.Get("Content-Type"), "image/") {
		http.Error(w, "aircraft image unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", imageResponse.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, imageResponse.Body); err != nil {
		log.Printf("aircraft image copy failed for aircraft=%s: %v", icao, err)
	}
}

func lookupAircraftPhoto(ctx context.Context, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", aircraftPhotoUserAgent)
	request.Header.Set("Accept", "application/json")
	response, err := aircraftImageHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PlaneSpotters returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Photos []struct {
			Thumbnail struct {
				Source string `json:"src"`
			} `json:"thumbnail"`
			ThumbnailLarge struct {
				Source string `json:"src"`
			} `json:"thumbnail_large"`
		} `json:"photos"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Photos) == 0 {
		return "", nil
	}
	if result.Photos[0].ThumbnailLarge.Source != "" {
		return result.Photos[0].ThumbnailLarge.Source, nil
	}
	return result.Photos[0].Thumbnail.Source, nil
}

func isPlaneSpottersImageHost(host string) bool {
	host = strings.ToLower(host)
	return host == "planespotters.net" || strings.HasSuffix(host, ".planespotters.net") || host == "plnspttrs.net" || strings.HasSuffix(host, ".plnspttrs.net")
}

func validAircraftRegistration(registration string) bool {
	if registration == "" {
		return true
	}
	if len(registration) > 12 {
		return false
	}
	for _, character := range registration {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

type webAircraft struct {
	ICAO               string   `json:"icao"`
	Raw                string   `json:"raw"`
	Frames             int      `json:"frames"`
	Downlink           int      `json:"downlinkFormat"`
	MessageType        int      `json:"messageType"`
	PositionReady      bool     `json:"positionReady"`
	Registration       string   `json:"registration,omitempty"`
	Manufacturer       string   `json:"manufacturer,omitempty"`
	Model              string   `json:"model,omitempty"`
	TypeCode           string   `json:"typecode,omitempty"`
	Operator           string   `json:"operator,omitempty"`
	Country            string   `json:"country,omitempty"`
	Callsign           string   `json:"callsign,omitempty"`
	DepartureAirport   string   `json:"departureAirport,omitempty"`
	DestinationAirport string   `json:"destinationAirport,omitempty"`
	Altitude           *int     `json:"altitudeFeet,omitempty"`
	Speed              *float64 `json:"speedKnots,omitempty"`
	Heading            *float64 `json:"headingDeg,omitempty"`
	Vertical           *int     `json:"verticalFPM,omitempty"`
	Latitude           *float64 `json:"latitude,omitempty"`
	Longitude          *float64 `json:"longitude,omitempty"`
	PositionTime       string   `json:"positionTimestamp,omitempty"`
	PositionReference  string   `json:"positionReference,omitempty"`
}

func toWebAircraft(m adsb.Message) webAircraft {
	var w webAircraft
	w.ICAO, w.Raw, w.Frames, w.Downlink, w.MessageType = m.ICAO, m.Raw, m.Frames, m.DownlinkFormat, m.TypeCode
	w.PositionReady = m.Latitude != nil && m.Longitude != nil
	w.Callsign = m.Callsign
	w.DepartureAirport, w.DestinationAirport = m.DepartureAirport, m.DestinationAirport
	w.Altitude, w.Speed, w.Heading, w.Vertical = m.AltitudeFeet, m.SpeedKnots, m.HeadingDeg, m.VerticalFPM
	w.Latitude, w.Longitude = m.Latitude, m.Longitude
	if !m.PositionTime.IsZero() {
		w.PositionTime = m.PositionTime.UTC().Format(time.RFC3339Nano)
	}
	w.PositionReference = m.PositionReference
	w.Registration = metadataValue(m.Metadata, "registration", "manufacturer", "model", "typecode", "operator", "country")
	w.Manufacturer = metadataValue(m.Metadata, "manufacturer", "model", "typecode", "operator", "country")
	w.Model = metadataValue(m.Metadata, "model", "typecode", "operator", "country")
	w.TypeCode = metadataValue(m.Metadata, "typecode", "operator", "country")
	w.Operator = metadataValue(m.Metadata, "operator", "country")
	w.Country = metadataValue(m.Metadata, "country")
	return w
}

func metadataValue(metadata, name string, next ...string) string {
	prefix := name + "="
	start := strings.Index(metadata, prefix)
	if start < 0 {
		return ""
	}
	value := metadata[start+len(prefix):]
	for _, key := range next {
		if index := strings.Index(value, " "+key+"="); index >= 0 {
			value = value[:index]
		}
	}
	return strings.TrimSpace(value)
}

type cprValue struct {
	lat, lon int
	odd      bool
	received time.Time
}

type cprDecodeReference struct {
	latitude  float64
	longitude float64
	source    string
}

func updateCPR(m *adsb.Message, states map[string][2]*cprValue, receivedAt time.Time, reference cprDecodeReference) {
	p := states[m.ICAO]
	v := &cprValue{lat: m.CPRLat, lon: m.CPRLon, odd: m.CPROdd, received: receivedAt}
	if m.CPROdd {
		p[1] = v
	} else {
		p[0] = v
	}
	states[m.ICAO] = p
	if decodeGlobalCPR(m, p) {
		return
	}
	decodeLocalCPR(m, v, reference)
}

func decodeGlobalCPR(m *adsb.Message, p [2]*cprValue) bool {
	if p[0] == nil || p[1] == nil || p[0].received.Sub(p[1].received).Abs() > 10*time.Second {
		return false
	}
	ye, yo := float64(p[0].lat)/131072, float64(p[1].lat)/131072
	xe, xo := float64(p[0].lon)/131072, float64(p[1].lon)/131072
	j := math.Floor(59*ye - 60*yo + .5)
	latEven := 6 * (positiveMod(j, 60) + ye)
	if latEven >= 270 {
		latEven -= 360
	}
	latOdd := (360.0 / 59.0) * (positiveMod(j, 59) + yo)
	if latOdd >= 270 {
		latOdd -= 360
	}
	if cprNL(latEven) != cprNL(latOdd) {
		return false
	}
	latestOdd := p[1].received.After(p[0].received)
	lat := latEven
	if latestOdd {
		lat = latOdd
	}
	nl := cprNL(lat)
	ni := nl
	if latestOdd {
		ni = nl - 1
	}
	if ni < 1 {
		ni = 1
	}
	mm := math.Floor(xe*float64(nl-1) - xo*float64(nl) + .5)
	x := xe
	if latestOdd {
		x = xo
	}
	lon := 360 / float64(ni) * (positiveMod(mm, float64(ni)) + x)
	if lon > 180 {
		lon -= 360
	}
	m.Latitude, m.Longitude = &lat, &lon
	m.PositionTime = p[0].received
	if latestOdd {
		m.PositionTime = p[1].received
		m.PositionReference = "odd"
	} else {
		m.PositionReference = "even"
	}
	return true
}

func decodeLocalCPR(m *adsb.Message, value *cprValue, reference cprDecodeReference) bool {
	if reference.latitude < -90 || reference.latitude > 90 || reference.longitude < -180 || reference.longitude > 180 {
		return false
	}
	format := 0
	if value.odd {
		format = 1
	}
	y := float64(value.lat) / 131072
	x := float64(value.lon) / 131072
	dLat := 360 / float64(60-format)
	latitudeIndex := math.Floor(reference.latitude/dLat) + math.Floor(.5+positiveMod(reference.latitude, dLat)/dLat-y)
	latitude := dLat * (latitudeIndex + y)
	if latitude >= 270 {
		latitude -= 360
	}
	if latitude < -90 || latitude > 90 || math.Abs(latitude-reference.latitude) > dLat/2 {
		return false
	}
	longitudeZones := cprNL(latitude) - format
	if longitudeZones < 1 {
		longitudeZones = 1
	}
	dLon := 360 / float64(longitudeZones)
	longitudeIndex := math.Floor(reference.longitude/dLon) + math.Floor(.5+positiveMod(reference.longitude, dLon)/dLon-x)
	longitude := dLon * (longitudeIndex + x)
	if longitude > 180 {
		longitude -= 360
	} else if longitude < -180 {
		longitude += 360
	}
	if distanceMeters(reference.latitude, reference.longitude, latitude, longitude) > localCPRReferenceRangeMeters {
		return false
	}
	m.Latitude, m.Longitude = &latitude, &longitude
	m.PositionTime = value.received
	parity := "even"
	if value.odd {
		parity = "odd"
	}
	m.PositionReference = "local_" + reference.source + "_" + parity
	return true
}

type positionEvaluation struct {
	decision          string
	reason            string
	accepted          bool
	trackReset        bool
	elapsedSeconds    float64
	distanceMeters    float64
	impliedSpeedKnots float64
	allowedSpeedKnots float64
}

func evaluatePosition(previous, current adsb.Message) positionEvaluation {
	result := positionEvaluation{decision: "accepted", accepted: true, reason: "first_position", allowedSpeedKnots: allowedPositionSpeed(previous)}
	if previous.Latitude == nil || previous.Longitude == nil || previous.PositionTime.IsZero() || current.Latitude == nil || current.Longitude == nil {
		return result
	}
	elapsed := current.PositionTime.Sub(previous.PositionTime)
	result.elapsedSeconds = elapsed.Seconds()
	if elapsed <= 0 {
		result.decision = "rejected"
		result.reason = "non_increasing_timestamp"
		result.accepted = false
		return result
	}
	result.distanceMeters = distanceMeters(*previous.Latitude, *previous.Longitude, *current.Latitude, *current.Longitude)
	result.impliedSpeedKnots = result.distanceMeters / elapsed.Seconds() * 1.9438444924406
	if result.distanceMeters > maximumPositionJumpMeters {
		result.decision = "rejected"
		result.reason = "jump_exceeds_distance_limit"
		result.accepted = false
		return result
	}
	if result.distanceMeters <= positionErrorAllowanceMeters {
		if elapsed >= trackGap {
			result.reason = "track_gap"
			result.trackReset = true
		} else {
			result.reason = "within_position_tolerance"
		}
		return result
	}
	if result.impliedSpeedKnots > result.allowedSpeedKnots {
		result.decision = "rejected"
		result.reason = "implied_speed_exceeds_limit"
		result.accepted = false
		return result
	}
	if elapsed >= trackGap {
		result.reason = "track_gap"
		result.trackReset = true
		return result
	}
	result.reason = "plausible_movement"
	return result
}

func allowedPositionSpeed(previous adsb.Message) float64 {
	allowed := unknownGroundSpeedLimitKnots
	if previous.SpeedKnots != nil {
		allowed = *previous.SpeedKnots*1.75 + 100
	}
	allowed = math.Max(100, allowed)
	return math.Min(allowed, maximumGroundSpeedKnots)
}

func validPosition(previous, current adsb.Message) bool {
	return evaluatePosition(previous, current).accepted
}

func makeTrackingEntry(receivedAt time.Time, decoded, previous adsb.Message, previousExists bool, stored adsb.Message, evaluation positionEvaluation, pair [2]*cprValue) tracking.Entry {
	entry := tracking.Entry{
		Event:              "adsb_frame",
		ReceivedAt:         receivedAt,
		ICAO:               decoded.ICAO,
		Registration:       metadataValue(stored.Metadata, "registration", "manufacturer", "model", "typecode", "operator", "country"),
		Callsign:           stored.Callsign,
		DepartureAirport:   stored.DepartureAirport,
		DestinationAirport: stored.DestinationAirport,
		RawFrame:           decoded.Raw,
		DownlinkFormat:     decoded.DownlinkFormat,
		TypeCode:           decoded.TypeCode,
		AltitudeFeet:       stored.AltitudeFeet,
		ReportedSpeedKnots: stored.SpeedKnots,
		HeadingDegrees:     stored.HeadingDeg,
		PositionDecision:   evaluation.decision,
		DecisionReason:     evaluation.reason,
		TrackReset:         evaluation.trackReset,
		CPRReference:       decoded.PositionReference,
	}
	if decoded.HasCPR {
		entry.CPR = &tracking.CPR{Latitude: decoded.CPRLat, Longitude: decoded.CPRLon, Odd: decoded.CPROdd}
	}
	if pair[0] != nil {
		entry.EvenCPR = &tracking.CPRSample{Latitude: pair[0].lat, Longitude: pair[0].lon, ReceivedAt: pair[0].received}
	}
	if pair[1] != nil {
		entry.OddCPR = &tracking.CPRSample{Latitude: pair[1].lat, Longitude: pair[1].lon, ReceivedAt: pair[1].received}
	}
	entry.DecodedPosition = trackingPosition(decoded)
	if previousExists {
		entry.PreviousPosition = trackingPosition(previous)
	}
	entry.StoredPosition = trackingPosition(stored)
	if decoded.Latitude != nil && previousExists && previous.Latitude != nil {
		entry.ElapsedSeconds = floatPointer(evaluation.elapsedSeconds)
		entry.DistanceMeters = floatPointer(evaluation.distanceMeters)
		entry.ImpliedSpeedKnots = floatPointer(evaluation.impliedSpeedKnots)
		entry.AllowedSpeedKnots = floatPointer(evaluation.allowedSpeedKnots)
	}
	return entry
}

func trackingPosition(message adsb.Message) *tracking.Position {
	if message.Latitude == nil || message.Longitude == nil || message.PositionTime.IsZero() {
		return nil
	}
	return &tracking.Position{Latitude: *message.Latitude, Longitude: *message.Longitude, Timestamp: message.PositionTime}
}

func floatPointer(value float64) *float64 { return &value }

func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6_371_000.0
	phi1, phi2 := lat1*math.Pi/180, lat2*math.Pi/180
	dPhi, dLambda := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dPhi/2)*math.Sin(dPhi/2) + math.Cos(phi1)*math.Cos(phi2)*math.Sin(dLambda/2)*math.Sin(dLambda/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func expireFlights(flights map[string]adsb.Message, now time.Time) {
	for icao, flight := range flights {
		if flight.LastSeen.IsZero() || now.Sub(flight.LastSeen) > aircraftExpiry {
			delete(flights, icao)
		}
	}
}

func expireCPR(states map[string][2]*cprValue, now time.Time) {
	for icao, pair := range states {
		for i, value := range pair {
			if value != nil && now.Sub(value.received) > cprExpiry {
				pair[i] = nil
			}
		}
		if pair[0] == nil && pair[1] == nil {
			delete(states, icao)
		} else {
			states[icao] = pair
		}
	}
}

func positiveMod(value, divisor float64) float64 {
	return math.Mod(math.Mod(value, divisor)+divisor, divisor)
}
func cprNL(x float64) int {
	latitude := math.Abs(x)
	if latitude < 1e-9 {
		return 59
	}
	if math.Abs(latitude-87) < 1e-9 {
		return 2
	}
	if latitude > 87 {
		return 1
	}
	cosine := math.Cos(latitude * math.Pi / 180)
	angle := math.Acos(1 - (1-math.Cos(math.Pi/30))/(cosine*cosine))
	zones := int(math.Floor(2 * math.Pi / angle))
	return max(1, min(59, zones))
}

func beep(mute bool) {
	if mute {
		return
	}
	bellMu.Lock()
	if bellPlaying {
		bellMu.Unlock()
		return
	}
	bellPlaying = true
	bellMu.Unlock()
	go func() {
		defer func() {
			bellMu.Lock()
			bellPlaying = false
			bellMu.Unlock()
		}()
		playBell()
	}()
}

func playBell() {
	soundLookup.Do(func() {
		players := []struct {
			name string
			args []string
		}{
			{name: "pw-play", args: []string{bellSound}},
			{name: "paplay", args: []string{bellSound}},
			{name: "canberra-gtk-play", args: []string{"--file", bellSound}},
			{name: "gst-play-1.0", args: []string{"--quiet", "--no-interactive", bellSound}},
			{name: "ffplay", args: []string{"-nodisp", "-autoexit", "-loglevel", "quiet", bellSound}},
		}
		for _, player := range players {
			if path, err := exec.LookPath(player.name); err == nil {
				soundPlayers = append(soundPlayers, bellCommand{path: path, args: player.args})
			}
		}
	})
	for index, player := range soundPlayers {
		ctx, cancel := context.WithTimeout(context.Background(), bellPlayerTimeout)
		command := exec.CommandContext(ctx, player.path, player.args...)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		err := command.Run()
		cancel()
		if err == nil {
			if index > 0 {
				soundPlayers[0], soundPlayers[index] = soundPlayers[index], soundPlayers[0]
			}
			return
		}
	}
	// Last resort for terminals with an enabled audible bell.
	soundFailureLog.Do(func() {
		log.Printf("audio players could not play %s; using the terminal bell", bellSound)
	})
	fmt.Fprint(os.Stderr, "\a")
}

func startupBeep(mute bool) {
	beep(mute)
}

// merge keeps the latest raw frame and accumulates fields spread across
// multiple ADS-B messages from the same aircraft.
func merge(old, current adsb.Message) adsb.Message {
	sameFlight := current.Callsign == "" || old.Callsign == "" || current.Callsign == old.Callsign
	if current.Callsign == "" {
		current.Callsign = old.Callsign
	}
	if sameFlight {
		if current.DepartureAirport == "" {
			current.DepartureAirport = old.DepartureAirport
		}
		if current.DestinationAirport == "" {
			current.DestinationAirport = old.DestinationAirport
		}
	}
	if current.Metadata == "" {
		current.Metadata = old.Metadata
	}
	if current.Latitude == nil {
		current.Latitude, current.Longitude = old.Latitude, old.Longitude
		current.PositionTime = old.PositionTime
		current.PositionReference = old.PositionReference
	}
	if current.AltitudeFeet == nil {
		current.AltitudeFeet = old.AltitudeFeet
	}
	if current.SpeedKnots == nil {
		current.SpeedKnots = old.SpeedKnots
	}
	if current.HeadingDeg == nil {
		current.HeadingDeg = old.HeadingDeg
	}
	if current.VerticalFPM == nil {
		current.VerticalFPM = old.VerticalFPM
	}
	current.Frames = old.Frames + 1
	return current
}
