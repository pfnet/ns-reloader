FROM golang:1.24 AS builder

ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X main.version=${BUILD_VERSION} -X main.commit=${BUILD_COMMIT}" \
    -o reloader

FROM scratch AS export
COPY --from=builder /workspace/reloader /reloader
ENTRYPOINT ["/reloader"]
