FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/discover ./cmd/discover
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/worker ./cmd/worker

FROM alpine:latest AS runtime-base

RUN apk --no-cache add ca-certificates

WORKDIR /root/

FROM runtime-base AS api

COPY --from=builder /out/server ./server
ENTRYPOINT ["./server"]

FROM runtime-base AS worker

COPY --from=builder /out/worker ./worker
ENTRYPOINT ["./worker"]

FROM runtime-base AS discover

COPY --from=builder /out/discover ./discover
ENTRYPOINT ["./discover"]
