.PHONY: build test test-race lint clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "(devel)")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o loglens .

test:
	go test ./... -v

test-race:
	go test ./... -race

lint:
	golangci-lint run

clean:
	rm -f loglens
