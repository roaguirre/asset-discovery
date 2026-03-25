.PHONY: validate build test contract-test test-e2e generate

build:
	go build -o discover cmd/discover/main.go

generate:
	go generate ./internal/discovery

test:
	go test -v ./...

contract-test:
	go test -v ./internal/export/visualizer -run 'TestContract'

test-e2e: build
	# Manual integration check; keep this out of the default validate/CI path.
	# Default outputs now archive each run under exports/runs/<run-id>/ and write visualizer data under exports/visualizer/.
	./discover --seeds test.json

validate: build test contract-test
	go vet ./...
	go fmt ./...
