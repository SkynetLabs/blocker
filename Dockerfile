FROM golang:1.17.7 as builder
LABEL maintainer="SkynetLabs <devs@skynetlabs.com>"

WORKDIR /root

COPY api api
COPY blocker blocker
COPY database database
COPY skyd skyd
COPY go.mod go.sum main.go Makefile ./

RUN go mod download && make release

FROM golang:1.17.7-alpine
LABEL maintainer="SkynetLabs <devs@skynetlabs.com>"

COPY --from=builder /go/bin/blocker /go/bin/blocker

ENTRYPOINT ["blocker"]
