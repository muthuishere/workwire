FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hub ./cmd/hub

FROM scratch
COPY --from=build /hub /hub
ENV AGENTHUB_BIND=0.0.0.0:14411 AGENTHUB_DATA_DIR=/data
VOLUME /data
EXPOSE 14411
ENTRYPOINT ["/hub"]
