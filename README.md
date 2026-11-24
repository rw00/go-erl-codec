# go-erl-codec

Go <-> [Erlang External Term Format (ETF)](https://www.erlang.org/doc/apps/erts/erl_ext_dist.html) codec (`package etf`), zero external dependencies.

## Features
- `Marshal`/`Unmarshal` and streaming `Encoder`/`Decoder` (mirroring `encoding/json`)
- Erlang-native struct tags (`etf:"<name>:<key_type>[ => <value_type>][, required][, null=<atom>]"`)
- Arbitrary Erlang map keys, including mixed-type keys with identical names and non-comparable keys (`etf.Map`)
- Tuples (`etf.Tuple`, custom `Marshaler`/`Unmarshaler` required on `=> tuple`) and validated Erlang datetimes (`etf.DateTime`, `=> datetime`)
- Nested structs, embedded structs, typed lists (`list(T)`), maps, opaque passthrough (`etf.RawTerm`, `any`, `etf.Map`), and custom `Marshaler`/`Unmarshaler`
- Safe type coercion (bounds-checked integer conversions, lossless float -> int, `STRING_EXT` -> numeric slices/charlists)
- Transparent zlib decompression, opt-in compression, and DoS safety limits

## API Terminology

- **`Marshal` / `Unmarshal`**: In-memory encoding and decoding between Go values and a single ETF-encoded `[]byte` (prefixed with version byte).
- **`Encoder.Encode` / `Decoder.Decode`**: Stream-oriented encoding and decoding over an `io.Writer` or `io.Reader`, writing or reading one or more sequential ETF terms directly on the stream.

## Quick Example

```go
// Matches Erlang:
// #{ user_id => ~"abc123", ~"user_id" => ~"abc123", amount => 41.5, currency => ~"EUR", capture => true, tags => [vip, recurring], request_info => #{ip => ~"127.0.0.1"} }
type PaymentRequest struct {
    UserID       string      `etf:"user_id:atom => binary, required"`
    LegacyUserID string      `etf:"user_id:binary => binary, required"`
    Amount       float64     `etf:"amount:atom => number, required"`
    Currency     string      `etf:"currency:atom, required"`
    Capture      bool        `etf:"capture:atom => boolean, required"`
    Tags         []string    `etf:"tags:atom => list(atom)"`
    RequestInfo  etf.RawTerm `etf:"request_info:atom => map"`
}

raw, err := etf.Marshal(req)
err = etf.Unmarshal(raw, &dst)
```

## Specification

See [SPEC.md](SPEC.md) for the full struct tag syntax, key/value type mappings, coercion rules, and error types, and the official [Erlang External Term Format](https://www.erlang.org/doc/apps/erts/erl_ext_dist.html) specification for wire format details.

## Running Tests

### Go Unit & Fuzz Tests
```bash
go test -v -race ./...
go test -fuzz=FuzzUnmarshal -fuzztime=10s .
```

### Docker Erlang/OTP 27 Round-Trip Interop Tests
```bash
docker build -f test/interop/Dockerfile -t go-erl-codec-interop .
docker run --rm go-erl-codec-interop
```
