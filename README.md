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
Aircraft with decoded CPR coordinates are shown at their received position;
aircraft waiting for a matching CPR pair are stacked above the receiver so
they remain visible. Click an aircraft marker for its readable metadata and
telemetry.

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
-mute           disable the short terminal beep for each valid message
-metadata       look up registration, model, operator, and country (default on)
-metadata-url   metadata endpoint base URL
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
and never prevent radio data from being decoded. Disable network lookups with
`-metadata=false`.

The receiver position is estimated from `https://ipapi.co/json/` when `-lat`
and `-lon` are omitted. For accuracy or privacy, provide both coordinates
manually. Aircraft latitude and longitude are calculated locally after an even
and an odd CPR position frame have been received.

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
accept/reject reason. Records with `event: "map_position_set"` are browser
acknowledgements created only when a new point is inserted into the Cesium 3D
world. They contain the browser timestamp and session ID, latitude, longitude,
altitude, Earth-centered Cartesian coordinates, and whether the even or odd CPR
frame was the position reference. Set `-tracking-dir` to another directory or
to an empty string to disable recording.

The application plays `/usr/share/sounds/freedesktop/stereo/bell.oga` through
PipeWire (`pw-play`) for startup and detection sounds when available. If
`pw-play` is unavailable, it falls back to the terminal bell. ADS-B messages
distribute aircraft data across several transmissions. The
application keeps one record per aircraft and carries previously received fields
forward, so later log lines show the accumulated callsign, altitude, speed,
heading, and vertical rate. `frames` is the number of valid messages received
for that aircraft and `raw` is the latest complete 112-bit frame. Position
requires pairing even and odd CPR frames and is outside this initial version.
