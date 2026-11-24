package main

import (
	"compress/zlib"
	"fmt"
	"math/big"
	"os"
	"strings"

	etf "github.com/rw00/go-erl-codec"
)

// Mixed atom/binary key payment request:
// #{ user_id => ~"abc123", ~"user_id" => ~"abc123", amount => 41.5, currency => ~"EUR" }
type PaymentRequest struct {
	UserID       string  `etf:"user_id:atom => binary, required"`
	LegacyUserID string  `etf:"user_id:binary => binary, required"`
	Amount       float64 `etf:"amount:atom => number, required"`
	Currency     string  `etf:"currency:atom => binary, required"`
}

type ShippingAddress struct {
	City       string `etf:"city:atom => binary, required"`
	PostalCode string `etf:"postal_code:atom => binary, required"`
}

type Extra struct {
	LegacyRef string `etf:"legacy_ref:charlist => charlist, required"`
	IntKey    string `etf:"100:integer => binary, required"`
	FloatKey  string `etf:"2.5:float => atom, required"`
	BoolKey   string `etf:"true:boolean => binary, required"`
}

type OrderRequest struct {
	OrderID      int64                 `etf:"order_id:atom => integer, required"`
	UserID       string                `etf:"user_id:atom => binary, required"`
	Capture      bool                  `etf:"capture:atom => boolean, required"`
	Tags         []string              `etf:"tags:atom => list(atom), required"`
	LineAmounts  []any                 `etf:"line_amounts:atom => list(number), required"`
	Installments [][]int               `etf:"installments:atom => list(list(integer)), required"`
	Metadata     map[string][]etf.Atom `etf:"metadata:atom => map, required"`
	Shipping     ShippingAddress       `etf:"shipping:atom, required"`
	OptNote      *string               `etf:"opt_note:atom => binary, required"`
	Extra        Extra                 `etf:"extra:atom => map, required"`
}

// CoercionStruct decodes Erlang integers into int8/int32/int64/uint64/float64
// and verifies round-trip back to declared Erlang types.
type CoercionStruct struct {
	AsInt8   int8    `etf:"a:atom => integer, required"`
	AsInt32  int32   `etf:"b:atom => integer, required"`
	AsInt64  int64   `etf:"c:atom => integer, required"`
	AsUint64 uint64  `etf:"d:atom => integer, required"`
	AsFloat  float64 `etf:"e:atom => integer, required"` // decoded from int into float64, encoded back as integer
	ExactFlt int64   `etf:"f:atom => float, required"`   // decoded from 42.0 into int64, encoded back as float
}

type customDelimited []string

func (c customDelimited) MarshalETF() ([]byte, error) {
	return etf.Marshal(etf.Binary(strings.Join(c, "|")))
}

func (c *customDelimited) UnmarshalETF(data []byte) error {
	var s string
	if err := etf.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*c = nil
		return nil
	}
	*c = strings.Split(s, "|")
	return nil
}

type CustomStruct struct {
	Items customDelimited `etf:"items:atom, required"`
	Raw   etf.RawTerm     `etf:"raw:atom, required"`
}

type customTuple struct {
	Status etf.Atom
	Code   int
	Msg    string
}

func (c customTuple) MarshalETF() ([]byte, error) {
	return etf.Marshal(etf.Tuple{c.Status, c.Code, etf.Binary(c.Msg)})
}

func (c *customTuple) UnmarshalETF(data []byte) error {
	var t etf.Tuple
	if err := etf.Unmarshal(data, &t); err != nil {
		return err
	}
	if len(t) != 3 {
		return fmt.Errorf("expected 3-tuple, got %d", len(t))
	}
	status, ok1 := t[0].(etf.Atom)
	code, ok2 := t[1].(int64)
	msg, ok3 := t[2].(etf.Binary)
	if !ok1 || !ok2 || !ok3 {
		return fmt.Errorf("unexpected customTuple element types: %#v", t)
	}
	*c = customTuple{Status: status, Code: int(code), Msg: string(msg)}
	return nil
}

type DateTimeStruct struct {
	CreatedAt   etf.DateTime  `etf:"created_at:atom => datetime, required"`
	LeapDay     etf.DateTime  `etf:"leap_day:atom => datetime, required"`
	OptAt       *etf.DateTime `etf:"opt_at:atom => datetime, required"`
	CustomTuple customTuple   `etf:"custom_tuple:atom => tuple, required"`
}

type PartialWorker struct {
	ID   int    `etf:"id:atom => integer, required"`
	Name string `etf:"name:atom => binary, required"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: %s <mode> <in.etf> <out.etf>\n", os.Args[0])
		os.Exit(2)
	}
	mode, inPath, outPath := os.Args[1], os.Args[2], os.Args[3]

	inBytes, err := os.ReadFile(inPath)
	if err != nil {
		fatalf("read %s: %v", inPath, err)
	}

	var outBytes []byte
	switch mode {
	case "any":
		var v any
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "compressed":
		var v any
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v, etf.WithCompression(zlib.BestCompression))

	case "scalar_int64":
		var v int64
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_uint64":
		var v uint64
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_bigint":
		var v *big.Int
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_float":
		var v float64
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_atom":
		var v etf.Atom
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_bool":
		var v bool
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_binary":
		var v []byte
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_bitstring":
		var v etf.Bitstring
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_charlist":
		var v etf.Charlist
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "scalar_int_slice":
		var v []int
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "payment_request":
		var v PaymentRequest
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "order_request":
		var v OrderRequest
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "coercion_struct":
		var v CoercionStruct
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "custom_struct":
		var v CustomStruct
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "datetime_struct":
		var v DateTimeStruct
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	case "ignore_unsupported":
		var v PartialWorker
		mustUnmarshal(inBytes, &v)
		outBytes = mustMarshal(v)

	default:
		fatalf("unknown mode %q", mode)
	}

	if err := os.WriteFile(outPath, outBytes, 0o600); err != nil {
		fatalf("write %s: %v", outPath, err)
	}
}

func mustUnmarshal(data []byte, v any) {
	if err := etf.Unmarshal(data, v); err != nil {
		fatalf("Unmarshal failed: %v", err)
	}
}

func mustMarshal(v any, opts ...etf.EncodeOption) []byte {
	out, err := etf.Marshal(v, opts...)
	if err != nil {
		fatalf("Marshal failed: %v", err)
	}
	return out
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
