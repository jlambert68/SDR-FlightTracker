.PHONY: build test run

build:
	go build -o bin/flighttracker ./cmd/flighttracker

test:
	go test ./...

run:
	go run ./cmd/flighttracker
