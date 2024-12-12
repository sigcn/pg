FROM golang:1.23-alpine AS builder
ADD . /pg
WORKDIR /pg
ARG version=unknown
ARG githash=unknown
RUN go build -ldflags "-s -w -X 'main.Version=$version' -X 'main.Commit=$githash'" ./cmd/pgcli
RUN go build -ldflags "-s -w -X 'main.Version=$version' -X 'main.Commit=$githash'" ./cmd/pgmap

FROM alpine:3.21
WORKDIR /root
COPY --from=builder /pg/pgcli /usr/bin/
COPY --from=builder /pg/pgmap /usr/bin/
