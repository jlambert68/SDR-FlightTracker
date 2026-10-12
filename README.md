# SDR Flight Tracker

A small Go application that receives 1090 MHz ADS-B messages with an RTL-SDR
dongle and logs decoded aircraft information to the console. It currently logs
ICAO addresses, callsigns, barometric altitude, ground speed, heading, and
vertical rate when those values are present in a received message.

## Requirements

- Go 1.22 or newer
- An RTL-SDR Blog V4 (or another RTL-SDR-compatible receiver)
- A 1090 MHz antenna, ideally placed near a window or outdoors
- The `rtl_adsb` program from the `rtl-sdr` package

On Debian or Ubuntu:

```sh
sudo apt update
sudo apt install rtl-sdr
```

If Linux has claimed the dongle as a TV receiver, blacklist the DVB kernel
driver and reboot:

```sh
echo 'blacklist dvb_usb_rtl28xxu' | sudo tee /etc/modprobe.d/rtl-sdr-blacklist.conf
```

Check that the device is visible before starting:

```sh
rtl_test -t
```

## Build and run

```sh
go build -o bin/flighttracker ./cmd/flighttracker
./bin/flighttracker
```

Open `http://localhost:8080` to view the live CesiumJS map. The Go process
serves the map and pushes the current aircraft list over Server-Sent Events.
Use the `Tottkällevägen 4A` button in the upper-right corner to return the
camera to the configured receiver position. This address is the default home
position; override it for another receiver site with `-lat` and `-lon`.
Drag with the left mouse button to turn the camera in place; the horizon stays
level while turning. Hold Shift while dragging to orbit around the globe instead.
Stockholm Arlanda and Stockholm Bromma are marked at their airport reference
points with a ground marker and a vertical line to a labeled 5,000 ft marker.
The purple Home sight volume is configured in `web/sight-config.json`. Its four
beams are listed in perimeter order (lower left, lower right, upper right,
upper left). `horizontalDegrees` is a compass bearing clockwise from true north,
and `verticalDegrees` is an angle above the local horizon; negative vertical
angles point below it. `rangeMeters` controls the length of all four beams. The
map's Home sight volume panel updates these values immediately and saves the
browser's settings locally. **Reset sight volume** restores the JSON defaults.
The supplied 240°–300° horizontal and 0°–45° vertical limits are an editable
west-facing starting point; set them to the measured limits of the real view.
While at least one current aircraft position is inside those horizontal,
vertical, and range limits, the map requests a bell every five seconds and the
panel shows the number of aircraft inside. The bell stops as soon as no current
position is inside or the live map connection closes. **Test bell** plays the
same desktop sound used for aircraft alerts; browser audio remains available as
a fallback if the desktop sound request fails.
Aircraft with decoded CPR coordinates are shown at their received position;
aircraft without a position remain visible in the aircraft list. Click an
aircraft marker for its readable metadata and telemetry. Between received
positions, amber dots show a five-second extrapolation from the last received
position using the ground speed and heading captured when that sequence starts.
Later velocity-only messages cannot turn an active extrapolated path. A new
received position clears all of that aircraft's extrapolated dots and starts a
new sequence without moving the received aircraft marker or its red track.
When a time-matched internet position is at least 500 metres from the active
extrapolation, the map stops adding amber dots until a new position is received.
Every position received from the local signal remains marked with a red dot.
At the first and every later received position, the map refreshes the preceding
five minutes of the aircraft's ADSB.lol trace and adds new points to the brown
history already displayed. When that history covers the interval between two
received positions, it replaces the provisional straight red line between
them; red fallback lines remain wherever the internet trace has not caught up.
While the aircraft remains current, the map refreshes its internet trace every
16 seconds, including positions newer than the latest receiver signal. Those
updates extend the brown path without moving the received aircraft marker.
Set `-history-duration` to another Go duration such as `10m`, or to `0` to
disable online history.

Example output:

```text
2026/10/07 18:42:01.123456 listening for ADS-B on 1090 MHz with RTL-SDR device 0
2026/10/07 18:42:03.456789 aircraft=4840D6 frames=1 df=17 type=1 raw=8D4840D6202CC371C32CE0576098 callsign=KLM1023
2026/10/07 18:42:04.123456 aircraft=40621D frames=2 df=17 type=11 raw=8D40621D58C382D690C8AC2863A7 altitude=38000ft
```

Useful options:

```text
-device 0       device index or serial number
-gain auto      automatic gain, or a value in dB such as 38.6
-ppm 0          tuner frequency correction
-bias-tee       enable bias power for an active antenna (use with care)
-mute           disable startup and Home sight-volume bells
-metadata       look up registration, model, operator, and country (default on)
-metadata-url   metadata endpoint base URL
-routes         look up departure and destination airports (default on)
-route-url      flight route endpoint base URL
-history-duration  online history window refreshed at each local position (default: 5m; 0 disables)
-history-url       ADSB.lol-compatible history service URL
-lat            receiver latitude (otherwise estimated from public IP)
-lon            receiver longitude (otherwise estimated from public IP)
-tracking-dir   per-aircraft tracking log directory (default: tracking-data)
```

For example:

```sh
./bin/flighttracker -device 0 -gain 38.6 -ppm 1
```

For each new ICAO address the program makes one best-effort request to the
HexDB aircraft metadata endpoint. When available, log lines include fields
such as `registration`, `manufacturer`, `model`, `operator`, and `country`.
Lookups are cached for the lifetime of the process, have a three-second timeout,
and never prevent radio data from being decoded. Disable aircraft metadata
lookups with `-metadata=false`.

When an aircraft transmits a callsign, the program makes one cached route lookup
through the public ADSBDB endpoint. The information panel and tracking JSON then
show the departure and destination airport names with their IATA and ICAO codes.
Route information cannot be determined when no callsign has been received.
Disable these lookups with `-routes=false`.

Flight history is refreshed at every newly received position through the Go
server, which avoids browser cross-origin restrictions and briefly caches the
upstream trace for multiple map clients. The browser merges each response into
the history already drawn. Current aircraft use ADSB.lol's recent trace feed;
replayed older data uses the corresponding daily archive when it is available.
History failures do not interrupt local ADS-B tracking or remove its fallback
line.

The receiver position is estimated from `https://ipapi.co/json/` when `-lat`
and `-lon` are omitted. For accuracy or privacy, provide both coordinates
manually. A recent even/odd CPR pair provides a globally decoded position. A
single position frame is decoded locally from the receiver position or the
aircraft's last accepted position, so dropped pairs do not leave large holes in
the track.

Failures are reported with timestamps in the log. Messages such as `metadata
lookup failed` include the aircraft address, endpoint, and HTTP or network error.
RTL-SDR startup, output, USB permission, missing-device, and unexpected decoder
exit failures are also reported explicitly.

Press Ctrl+C to stop. To test the decoder without radio hardware, save lines in
the `*8D...;` format produced by `rtl_adsb` and run:

```sh
./bin/flighttracker -input messages.txt
```

## Tracking diagnostics

Every valid ADS-B frame is recorded as newline-delimited JSON for later path
analysis. The default layout uses the aircraft's stable ICAO address as its
folder name and starts a new file each UTC day:

```text
tracking-data/
  4CAFD6/
    2026-10-08.jsonl
```

Each line is a complete JSON object containing the raw frame, even/odd CPR
values and timestamps, decoded and previously accepted positions, distance and
elapsed time, implied and allowed speeds, the stored position, and the exact
accept/reject reason. When available, records also contain the departure and
destination airports resolved from the callsign. Records with
`event: "map_position_set"` are browser
acknowledgements created only when a new point is inserted into the Cesium 3D
world. They contain the browser timestamp and session ID, latitude, longitude,
altitude, Earth-centered Cartesian coordinates, and the CPR decoding reference
(global pair, receiver, or previous aircraft position). Set `-tracking-dir` to
another directory or to an empty string to disable recording.

The startup bell, Home sight-volume alarm, and **Test bell** button play
`/usr/share/sounds/freedesktop/stereo/bell.oga`. The server tries `pw-play`,
`paplay`, `canberra-gtk-play`, `gst-play-1.0`, and `ffplay` in order, and falls
back to the terminal bell if the installed player cannot reach the audio system.
The live alarm can also use browser audio as a fallback after a click or key press.
ADS-B messages distribute aircraft data across several transmissions. The
application keeps one record per aircraft and carries previously received fields
forward, so later log lines show the accumulated callsign, altitude, speed,
heading, and vertical rate. `frames` is the number of valid messages received
for that aircraft and `raw` is the latest complete 112-bit frame.
