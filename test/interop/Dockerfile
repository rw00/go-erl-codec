FROM golang:1.24-alpine AS go-builder

WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY test/interop/cmd/etf-roundtrip ./test/interop/cmd/etf-roundtrip

RUN go test -v ./...
RUN CGO_ENABLED=0 go build -o /out/etf-roundtrip ./test/interop/cmd/etf-roundtrip

FROM erlang:27-alpine

COPY --from=go-builder /out/etf-roundtrip /usr/local/bin/etf-roundtrip
COPY test/interop/verify.escript /app/verify.escript
RUN chmod +x /usr/local/bin/etf-roundtrip /app/verify.escript

ENTRYPOINT ["escript", "/app/verify.escript"]
