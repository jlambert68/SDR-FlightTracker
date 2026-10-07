package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
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
	"sdr-flight-tracker/internal/location"
)

const bellSound = "/usr/share/sounds/freedesktop/stereo/bell.oga"
const homeAddress = "Tottkällevägen 4A, Värmdö"
const homeLatitude = 59.323316
const homeLongitude = 18.480349

var soundLookup sync.Once
var soundPlayer string
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
	input       string
	latitude    float64
	longitude   float64
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	var opts options
	flag.StringVar(&opts.device, "device", "0", "RTL-SDR device index or serial number")
	flag.StringVar(&opts.gain, "gain", "auto", "tuner gain in dB, or auto")
	flag.IntVar(&opts.ppm, "ppm", 0, "frequency correction in PPM")
	flag.BoolVar(&opts.biasTee, "bias-tee", false, "enable the dongle bias tee")
	flag.BoolVar(&opts.mute, "mute", false, "disable the terminal beep for received messages")
	flag.BoolVar(&opts.metadata, "metadata", true, "look up readable aircraft information online")
	flag.StringVar(&opts.metadataURL, "metadata-url", "https://hexdb.io/api/v1/aircraft", "aircraft metadata service URL")
	flag.StringVar(&opts.input, "input", "", "read rtl_adsb text from a file, or - for stdin")
	flag.Float64Var(&opts.latitude, "lat", homeLatitude, "receiver latitude")
	flag.Float64Var(&opts.longitude, "lon", homeLongitude, "receiver longitude")
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
	go serveWeb()

	if err := run(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("flight tracker: %v", err)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.input != "" {
		if opts.input == "-" {
			return consume(os.Stdin, opts.mute, opts)
		}
		f, err := os.Open(opts.input)
		if err != nil {
			return err
		}
		defer f.Close()
		return consume(f, opts.mute, opts)
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
	scanErr := consume(stdout, opts.mute, opts)
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

func consume(r io.Reader, mute bool, opts options) error {
	flights := make(map[string]adsb.Message)
	cpr := make(map[string][2]*cprValue)
	metadataClient := aircraft.NewClient(opts.metadataURL)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		message, err := adsb.DecodeLine(raw)
		if err != nil {
			continue
		}
		if opts.metadata {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var lookupErr error
			message.Metadata, lookupErr = metadataClient.Lookup(ctx, message.ICAO)
			if lookupErr != nil {
				log.Printf("metadata lookup failed for aircraft=%s endpoint=%s: %v", message.ICAO, opts.metadataURL, lookupErr)
			}
			cancel()
		}
		if message.TypeCode >= 9 && message.TypeCode <= 18 {
			updateCPR(&message, cpr)
		}
		// The ASCII bell is handled by most terminal emulators as a short beep.
		// It is emitted only after CRC validation, so radio noise stays silent.
		beep(mute)
		previous, exists := flights[message.ICAO]
		if exists {
			message = merge(previous, message)
		}
		flights[message.ICAO] = message
		flightMu.Lock()
		liveFlights = flights
		flightMu.Unlock()
		log.Printf("%s", message)
	}
	return scanner.Err()
}

func serveWeb() {
	http.Handle("/", http.FileServer(http.Dir("web")))
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
			flightMu.RLock()
			aircraftList := make([]webAircraft, 0, len(liveFlights))
			for _, flight := range liveFlights {
				aircraftList = append(aircraftList, toWebAircraft(flight))
			}
			payload := map[string]any{"receiver": receiverPosition, "aircraft": aircraftList}
			data, _ := json.Marshal(payload)
			flightMu.RUnlock()
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

type webAircraft struct {
	ICAO          string   `json:"icao"`
	Raw           string   `json:"raw"`
	Frames        int      `json:"frames"`
	Downlink      int      `json:"downlinkFormat"`
	MessageType   int      `json:"messageType"`
	PositionReady bool     `json:"positionReady"`
	Registration  string   `json:"registration,omitempty"`
	Manufacturer  string   `json:"manufacturer,omitempty"`
	Model         string   `json:"model,omitempty"`
	TypeCode      string   `json:"typecode,omitempty"`
	Operator      string   `json:"operator,omitempty"`
	Country       string   `json:"country,omitempty"`
	Callsign      string   `json:"callsign,omitempty"`
	Altitude      *int     `json:"altitudeFeet,omitempty"`
	Speed         *float64 `json:"speedKnots,omitempty"`
	Heading       *float64 `json:"headingDeg,omitempty"`
	Vertical      *int     `json:"verticalFPM,omitempty"`
	Latitude      *float64 `json:"latitude,omitempty"`
	Longitude     *float64 `json:"longitude,omitempty"`
}

func toWebAircraft(m adsb.Message) webAircraft {
	var w webAircraft
	w.ICAO, w.Raw, w.Frames, w.Downlink, w.MessageType = m.ICAO, m.Raw, m.Frames, m.DownlinkFormat, m.TypeCode
	w.PositionReady = m.Latitude != nil && m.Longitude != nil
	w.Callsign = m.Callsign
	w.Altitude, w.Speed, w.Heading, w.Vertical = m.AltitudeFeet, m.SpeedKnots, m.HeadingDeg, m.VerticalFPM
	w.Latitude, w.Longitude = m.Latitude, m.Longitude
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

func updateCPR(m *adsb.Message, states map[string][2]*cprValue) {
	p := states[m.ICAO]
	v := &cprValue{lat: m.CPRLat, lon: m.CPRLon, odd: m.CPROdd, received: time.Now()}
	if m.CPROdd {
		p[1] = v
	} else {
		p[0] = v
	}
	states[m.ICAO] = p
	if p[0] == nil || p[1] == nil {
		return
	}
	if p[0].received.Sub(p[1].received).Abs() > 10*time.Second {
		return
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
		return
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
}

func positiveMod(value, divisor float64) float64 {
	return math.Mod(math.Mod(value, divisor)+divisor, divisor)
}
func cprNL(x float64) int {
	a := math.Abs(x)
	table := []float64{10.47047130, 14.82817437, 18.18626357, 21.02939493, 23.54504487, 25.82924707, 27.93898710, 29.91135686, 31.77209708, 33.53993436, 35.22899598, 36.85025108, 38.41241892, 39.92256684, 41.38651832, 42.80914012, 44.19454951, 45.54626723, 46.86733252, 48.16039128, 49.42657329, 50.67430654, 51.88118079, 53.09030787, 54.28372143, 55.51114624, 56.74896827, 57.99123265, 59.29165945, 60.42375649, 61.55915745, 62.74216572, 63.86768462, 64.99578747, 66.08326154, 67.17905545, 68.15212705, 69.17814108, 70.17075792, 71.06688139, 71.99176150, 72.93072889, 73.86700662, 74.74713461, 75.70491253, 76.48024657, 77.30657830, 78.07284587, 78.83312199, 79.44456549, 80.16988713, 80.82657905, 81.32875431, 81.99087188, 82.73999104, 83.99196815, 85}
	for i, v := range table {
		if a < v {
			return 59 - i
		}
	}
	return 1
}

func beep(mute bool) {
	if mute {
		return
	}
	soundLookup.Do(func() {
		soundPlayer, _ = exec.LookPath("pw-play")
	})
	if soundPlayer != "" {
		// Play asynchronously so a burst of ADS-B messages never blocks decoding.
		player := exec.Command(soundPlayer, bellSound)
		if err := player.Start(); err == nil {
			go func() { _ = player.Wait() }()
			return
		}
	}
	// Fallback for terminals with an enabled audible bell.
	fmt.Fprint(os.Stderr, "\a")
}

func startupBeep(mute bool) {
	if mute {
		return
	}
	beep(false)
	time.Sleep(120 * time.Millisecond)
	beep(false)
}

// merge keeps the latest raw frame and accumulates fields spread across
// multiple ADS-B messages from the same aircraft.
func merge(old, current adsb.Message) adsb.Message {
	if current.Callsign == "" {
		current.Callsign = old.Callsign
	}
	if current.Metadata == "" {
		current.Metadata = old.Metadata
	}
	if current.Latitude == nil {
		current.Latitude, current.Longitude = old.Latitude, old.Longitude
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
