FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /raftlite ./cmd/raftlite/ && \
    CGO_ENABLED=0 go build -o /simgw ./cmd/simgw/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /raftlite /raftlite
COPY --from=builder /simgw /simgw
USER nobody
ENTRYPOINT ["/raftlite"]
