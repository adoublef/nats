# syntax=docker/dockerfile:1

ARG VERSION_GO=1.21
ARG VERSION_ALPINE=3.18

FROM golang:${VERSION_GO}-alpine${VERSION_ALPINE} AS build

WORKDIR /usr/src

COPY go.* .
RUN go mod download

# required for go-sqlite3
RUN apk add --no-cache gcc musl-dev

COPY . .

ENV CGO_ENABLED=1
ENV GOOS=linux
ENV GOARCH=amd64
RUN go build \
    -ldflags "-s -w -extldflags '-static'" \
    -buildvcs=false \
    -tags osusergo,netgo \
    -o /usr/local/bin/ ./...

FROM alpine:${VERSION_ALPINE} AS runtime

WORKDIR /opt

ARG EXE_NAME="client"
COPY --from=build /usr/local/bin/${EXE_NAME} ./a

CMD ["./a"]