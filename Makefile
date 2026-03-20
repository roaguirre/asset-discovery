.PHONY: validate test test-e2e build generate

build:
	go build -o discover cmd/discover/main.go

generate:
	go generate ./internal/discovery

test:
	go test -v ./...

test-e2e: build
	# Default outputs now archive each run under exports/runs/<run-id>/ and refresh exports/visualizer.html.
	./discover --seeds test.json

validate: build test test-e2e
	go vet ./...
	go fmt ./...
