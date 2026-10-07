# Static binary on a minimal base: CA certificates and a non-root user, nothing else.
#   docker build -t radio-spinlog .
#   docker run -d --name radio-spinlog -v radio-spinlog-data:/data radio-spinlog
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /radio-spinlog ./cmd/radio-spinlog \
    && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /radio-spinlog /radio-spinlog
# SQLite archive, writable by the non-root user; unused when RADIO_SPINLOG_DB is a postgres:// URL
COPY --from=build --chown=nonroot:nonroot /data /data
VOLUME /data
ENV RADIO_SPINLOG_DB=/data/archive.db
ENTRYPOINT ["/radio-spinlog"]
