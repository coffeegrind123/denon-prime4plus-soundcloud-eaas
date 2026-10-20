FROM golang:1.24-alpine AS build

RUN apk add --no-cache ca-certificates git
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY . .

WORKDIR /src
RUN go mod download
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/soundcloud-eaas .

FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 eaas \
    && mkdir -p /var/cache/soundcloud-eaas \
    && chown eaas:eaas /var/cache/soundcloud-eaas
COPY --from=build /out/soundcloud-eaas /usr/local/bin/soundcloud-eaas
USER eaas
EXPOSE 11224/udp 50010/tcp 50020/tcp
ENTRYPOINT ["/usr/local/bin/soundcloud-eaas"]
