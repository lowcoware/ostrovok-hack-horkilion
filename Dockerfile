# One image per Go service: docker build --build-arg SERVICE=gateway .
FROM golang:1.27-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY gen ./gen
COPY internal ./internal
COPY migrations ./migrations
ARG SERVICE
RUN test -n "$SERVICE" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

# Empty image: the static binary plus CA certificates for outbound HTTPS
# (OpenRouter, Open-Meteo). DNS uses the pure Go resolver and the
# /etc/resolv.conf Docker mounts. Runs as an unprivileged user.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/app /app
# Business rules read by the agent at start (RULES_PATH, relative to /).
COPY config/rules.yaml /config/rules.yaml
# Data snapshot read by inventory at start (DATA_DIR=/data).
# impulse: the image carries the backend test snapshot, switch to data/snapshot once P1-DS-01 lands it in main.
COPY internal/dataset/testdata/snapshot /data
USER 65532:65532
ENTRYPOINT ["/app"]
