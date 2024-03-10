FROM golang:1.22-alpine AS builder
ADD . /peerguard
WORKDIR /peerguard
ARG version=unknown
ARG githash=unknown
RUN go build -ldflags "-s -w -X 'main.Version=$version' -X 'main.Commit=$githash'" ./cmd/pgcli
RUN go build -ldflags "-s -w -X 'main.Version=$version' -X 'main.Commit=$githash'" ./cmd/pgserve

FROM alpine:3.19
WORKDIR /root
COPY --from=builder /peerguard/pgcli /usr/bin/
COPY --from=builder /peerguard/pgserve /usr/bin/
