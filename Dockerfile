# Multi-stage build for local verification via docker compose.
# Production deployment uses a cross-compiled binary + systemd (no Docker).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/notes ./cmd/notes

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/notes /notes
VOLUME /data
ENV DATA_DIR=/data \
    LISTEN_ADDR=:8080 \
    RP_ID=localhost \
    RP_NAME="Carseph Notes" \
    ORIGIN=http://localhost:8080
EXPOSE 8080
ENTRYPOINT ["/notes"]
