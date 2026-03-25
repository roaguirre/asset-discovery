.PHONY: validate build test test-e2e test-firebase server generate

build:
	go build -o discover cmd/discover/main.go

generate:
	go generate ./internal/discovery

test:
	go test -v ./...

test-firebase:
	../asset-discovery-web/scripts/with-firebase-java.sh firebase emulators:exec --project demo-asset-discovery --config ../asset-discovery-web/firebase.json --only firestore "go test -v ./internal/runservice"

server:
	@set -a; \
	if [ -f .env.local ]; then . ./.env.local; fi; \
	set +a; \
	go run ./cmd/server

test-e2e: build
	# Manual integration check; keep this out of the default validate/CI path.
	./discover --seeds test.json

validate: build test
	go vet ./...
	go fmt ./...
