.PHONY: build test test-race lint clean

build:
	go build -o loglens .

test:
	go test ./... -v

test-race:
	go test ./... -race

lint:
	golangci-lint run

clean:
	rm -f loglens
