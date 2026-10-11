package adsb

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Message contains the fields that can be decoded from one ADS-B frame.
type Message struct {
	Raw                string
	DownlinkFormat     int
	ICAO               string
	TypeCode           int
	Frames             int
	Metadata           string
	CPRLat             int
	CPRLon             int
	CPROdd             bool
	HasCPR             bool
	Callsign           string
	DepartureAirport   string
	DestinationAirport string
	AltitudeFeet       *int
	SpeedKnots         *float64
	HeadingDeg         *float64
	VerticalFPM        *int
	Latitude           *float64
	Longitude          *float64
	PositionTime       time.Time
	PositionReference  string
	LastSeen           time.Time
}

func (m Message) String() string {
	parts := []string{fmt.Sprintf("aircraft=%s frames=%d df=%d type=%d", m.ICAO, m.Frames, m.DownlinkFormat, m.TypeCode)}
	if m.Raw != "" {
		parts = append(parts, "raw="+m.Raw)
	}
	if m.Metadata != "" {
		parts = append(parts, m.Metadata)
	}
	if m.Callsign != "" {
		parts = append(parts, "callsign="+m.Callsign)
	}
	if m.AltitudeFeet != nil {
		parts = append(parts, fmt.Sprintf("altitude=%dft", *m.AltitudeFeet))
	}
	if m.SpeedKnots != nil {
		parts = append(parts, fmt.Sprintf("speed=%.0fkt", *m.SpeedKnots))
	}
	if m.HeadingDeg != nil {
		parts = append(parts, fmt.Sprintf("heading=%.0fdeg", *m.HeadingDeg))
	}
	if m.VerticalFPM != nil {
		parts = append(parts, fmt.Sprintf("vertical_rate=%dfpm", *m.VerticalFPM))
	}
	if m.Latitude != nil && m.Longitude != nil {
		parts = append(parts, fmt.Sprintf("latitude=%.5f longitude=%.5f", *m.Latitude, *m.Longitude))
	} else if m.TypeCode >= 9 && m.TypeCode <= 18 {
		parts = append(parts, "position=pending_even_odd_cpr_pair")
	}
	return strings.Join(parts, " ")
}

// DecodeLine accepts the *HEX; format emitted by rtl_adsb.
func DecodeLine(line string) (Message, error) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "*")
	line = strings.TrimSuffix(line, ";")
	if len(line) != 28 {
		return Message{}, errors.New("expected a 112-bit Mode S frame")
	}
	frame, err := hex.DecodeString(line)
	if err != nil {
		return Message{}, fmt.Errorf("invalid hexadecimal frame: %w", err)
	}
	return Decode(frame)
}

func Decode(frame []byte) (Message, error) {
	if len(frame) != 14 {
		return Message{}, errors.New("expected a 14-byte frame")
	}
	if downlinkFormat(frame) != 17 {
		return Message{}, errors.New("frame is not an ADS-B extended squitter")
	}
	if remainder(frame) != 0 {
		return Message{}, errors.New("frame failed its CRC")
	}

	m := Message{
		Raw:            strings.ToUpper(hex.EncodeToString(frame)),
		DownlinkFormat: downlinkFormat(frame),
		ICAO:           strings.ToUpper(hex.EncodeToString(frame[1:4])),
		TypeCode:       bits(frame, 33, 5),
		Frames:         1,
	}
	switch {
	case m.TypeCode >= 1 && m.TypeCode <= 4:
		m.Callsign = decodeCallsign(frame)
	case m.TypeCode >= 9 && m.TypeCode <= 18:
		m.CPRLat = bits(frame, 55, 17)
		m.CPRLon = bits(frame, 72, 17)
		m.CPROdd = bits(frame, 54, 1) == 1
		m.HasCPR = true
		if altitude, ok := decodeAltitude(frame); ok {
			m.AltitudeFeet = &altitude
		}
	case m.TypeCode == 19:
		decodeVelocity(frame, &m)
	}
	return m, nil
}

func downlinkFormat(frame []byte) int { return int(frame[0] >> 3) }

// bits reads a one-based bit range, with bit 1 being the MSB of byte zero.
func bits(frame []byte, start, length int) int {
	v := 0
	for i := 0; i < length; i++ {
		position := start - 1 + i
		v = v<<1 | int((frame[position/8]>>uint(7-position%8))&1)
	}
	return v
}

func decodeCallsign(frame []byte) string {
	const charset = "#ABCDEFGHIJKLMNOPQRSTUVWXYZ#####_###############0123456789######"
	var b strings.Builder
	for i := 0; i < 8; i++ {
		c := charset[bits(frame, 41+i*6, 6)]
		if c == '_' {
			c = ' '
		}
		if c != '#' {
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

func decodeAltitude(frame []byte) (int, bool) {
	raw := bits(frame, 41, 12)
	if raw&0x10 == 0 { // Gillham-encoded altitude is intentionally left unsupported.
		return 0, false
	}
	n := ((raw & 0xFE0) >> 1) | (raw & 0x0F)
	return n*25 - 1000, true
}

func decodeVelocity(frame []byte, m *Message) {
	subtype := bits(frame, 38, 3)
	if subtype != 1 && subtype != 2 {
		return
	}
	scale := 1.0
	if subtype == 2 {
		scale = 4
	}
	ewRaw, nsRaw := bits(frame, 47, 10), bits(frame, 58, 10)
	if ewRaw > 0 && nsRaw > 0 {
		ew := float64(ewRaw-1) * scale
		ns := float64(nsRaw-1) * scale
		if bits(frame, 46, 1) == 1 {
			ew = -ew
		}
		if bits(frame, 57, 1) == 1 {
			ns = -ns
		}
		speed := math.Hypot(ew, ns)
		heading := math.Mod(math.Atan2(ew, ns)*180/math.Pi+360, 360)
		m.SpeedKnots, m.HeadingDeg = &speed, &heading
	}
	vrRaw := bits(frame, 70, 9)
	if vrRaw > 0 {
		vr := (vrRaw - 1) * 64
		if bits(frame, 69, 1) == 1 {
			vr = -vr
		}
		m.VerticalFPM = &vr
	}
}

func remainder(frame []byte) uint32 {
	data := append([]byte(nil), frame...)
	// The generator includes its x^24 leading term.
	poly := uint32(0x1FFF409)
	for i := 0; i < len(data)*8-24; i++ {
		if data[i/8]&(1<<uint(7-i%8)) == 0 {
			continue
		}
		for j := 0; j < 25; j++ {
			if poly&(1<<uint(24-j)) != 0 {
				position := i + j
				data[position/8] ^= 1 << uint(7-position%8)
			}
		}
	}
	return uint32(data[len(data)-3])<<16 | uint32(data[len(data)-2])<<8 | uint32(data[len(data)-1])
}
