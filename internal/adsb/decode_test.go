package adsb

import "testing"

func TestDecodeCallsign(t *testing.T) {
	m, err := DecodeLine("*8D4840D6202CC371C32CE0576098;")
	if err != nil {
		t.Fatal(err)
	}
	if m.ICAO != "4840D6" || m.Callsign != "KLM1023" {
		t.Fatalf("unexpected message: %+v", m)
	}
}

func TestDecodeAltitude(t *testing.T) {
	m, err := DecodeLine("8D40621D58C382D690C8AC2863A7")
	if err != nil {
		t.Fatal(err)
	}
	if m.AltitudeFeet == nil || *m.AltitudeFeet != 38000 {
		t.Fatalf("unexpected altitude: %+v", m.AltitudeFeet)
	}
}

func TestRejectsBadCRC(t *testing.T) {
	_, err := DecodeLine("8D4840D6202CC371C32CE0576099")
	if err == nil {
		t.Fatal("expected CRC error")
	}
}
