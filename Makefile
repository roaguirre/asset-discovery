.PHONY: validate build test test-e2e test-firebase server generate

build:
	go build -o discover cmd/discover/main.go

generate:
	go generate ./internal/discovery

test:
	go test -v ./...

test-firebase:
	./scripts/with-firebase-java.sh npx -y firebase-tools@latest emulators:exec --project demo-asset-discovery --config firebase.json --only firestore "go test -count=1 -tags=firestoreemulator -v ./internal/runservice"

server:
	@set -a; \
	if [ -f .env.local ]; then . ./.env.local; fi; \
	set +a; \
	go run ./cmd/server

test-e2e: build
	# Manual integration check; keep this out of the default validate/CI path.
	./discover --seeds test.json

validate: build test test-firebase
	go vet ./...
	go fmt ./...
