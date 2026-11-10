FROM golang:1.26-alpine AS build
WORKDIR /src
COPY combined_refactor/ /src/combined_refactor/
COPY auto/ /src/auto/
COPY shared/ /src/shared/
RUN cd /src/combined_refactor \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cfscan . \
 && cd /src/auto \
 && CGO_ENABLED=0 GOOS=linux go test ./... \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cfscan-auto .

FROM scratch
COPY --from=build /out/cfscan /usr/local/bin/cfscan
COPY --from=build /out/cfscan-auto /usr/local/bin/cfscan-auto
COPY combined_refactor/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
WORKDIR /data
VOLUME ["/data"]
EXPOSE 13335
ENTRYPOINT ["/usr/local/bin/cfscan-auto"]
