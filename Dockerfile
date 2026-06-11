# Builds every promhash binary into one small image.
# Used by the demo compose stack and usable as a deployment base.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM alpine:3.20
COPY --from=build /out/ /usr/local/bin/
# No default entrypoint: each compose service names its binary.
