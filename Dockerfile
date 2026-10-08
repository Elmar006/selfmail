FROM golang:1.26-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107 AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM source AS test
ENV CGO_ENABLED=1
CMD ["sh", "-c", "go test -race -count=1 ./... && go vet ./..."]

FROM source AS build
ARG VERSION=dev
ARG REVISION=unknown
RUN --mount=type=cache,id=selfmail-build,target=/root/.cache/go CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" -o /out/selfmail ./cmd/selfmail

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS runtime
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 selfmail && adduser -D -u 10001 -G selfmail selfmail
COPY --from=build /out/selfmail /usr/local/bin/selfmail
USER 10001:10001
ENTRYPOINT ["selfmail"]
CMD ["api"]
