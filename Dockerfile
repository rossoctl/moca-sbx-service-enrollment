# The enrollment sync: a static Go binary that shells out to git, so the runtime image
# needs git and CA certificates and nothing else.
FROM docker.io/library/golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /enrollment-sync ./cmd/enrollment-sync

FROM docker.io/library/alpine:3.20
RUN apk add --no-cache git ca-certificates && adduser -D -u 10001 enrollsync
USER 10001:10001
COPY --from=build /enrollment-sync /usr/local/bin/enrollment-sync
ENTRYPOINT ["enrollment-sync"]
