# workwire hub — one static binary, FROM scratch (ADR-006).
# State is exactly one directory: mount a volume at /data.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /workwire ./cmd/workwire

FROM scratch
COPY --from=build /workwire /workwire
ENV WORKWIRE_BIND=0.0.0.0 \
    WORKWIRE_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 14411
ENTRYPOINT ["/workwire", "serve"]
