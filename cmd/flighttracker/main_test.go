package main

import (
	"math"
	"testing"

	"sdr-flight-tracker/internal/adsb"
)

func TestUpdateCPRKnownPosition(t *testing.T) {
	states := make(map[string][2]*cprValue)
	even, err := adsb.DecodeLine("8D40621D58C382D690C8AC2863A7")
	if err != nil {
		t.Fatal(err)
	}
	updateCPR(&even, states)
	odd, err := adsb.DecodeLine("8D40621D58C386435CC412692AD6")
	if err != nil {
		t.Fatal(err)
	}
	updateCPR(&odd, states)
	if odd.Latitude == nil || odd.Longitude == nil {
		t.Fatal("position was not decoded")
	}
	if math.Abs(*odd.Latitude-52.26578) > .001 || math.Abs(*odd.Longitude-3.93891) > .001 {
		t.Fatalf("unexpected position: %.5f, %.5f", *odd.Latitude, *odd.Longitude)
	}
}
