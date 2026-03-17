.PHONY: validate test test-e2e build

build:
	go build -o discover cmd/discover/main.go

test:
	go test -v ./...

test-e2e: build
	./discover --seeds test.json

validate: build test test-e2e
	go vet ./...
	go fmt ./...
