package etf

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	ErrInvalidVersion       = errors.New("etf: invalid version byte")
	ErrUnexpectedEOF        = errors.New("etf: unexpected EOF")
	ErrTrailingBytes        = errors.New("etf: trailing bytes after term")
	ErrUnsupportedTag       = errors.New("etf: unsupported ETF tag")
	ErrTypeMismatch         = errors.New("etf: ETF wire type does not match expected type")
	ErrMissingRequiredField = errors.New("etf: missing required field")
	ErrUnknownField         = errors.New("etf: unknown field")
	ErrNonFiniteFloat       = errors.New("etf: non-finite float not allowed in ETF")
	ErrAtomTooLong          = errors.New("etf: atom exceeds 255 Unicode characters")
	ErrInvalidUTF8          = errors.New("etf: invalid UTF-8")
	ErrOverflow             = errors.New("etf: numeric overflow")
	ErrLossyCoercion        = errors.New("etf: lossy numeric coercion")
	ErrImproperList         = errors.New("etf: improper list cannot decode into slice")
	ErrMaxDepthExceeded     = errors.New("etf: maximum nesting depth exceeded")
	ErrMaxSizeExceeded      = errors.New("etf: maximum uncompressed size exceeded")
	ErrDuplicateMapKey      = errors.New("etf: duplicate key in map")
	ErrUncomparableMapKey   = errors.New("etf: non-comparable Erlang key for Go map (use etf.Map)")
	ErrInvalidBitstring     = errors.New("etf: invalid bitstring bit count")
	ErrInvalidBignumSign    = errors.New("etf: invalid bignum sign byte")
	ErrInvalidDateTime      = errors.New("etf: invalid Erlang datetime")
	ErrInvalidTarget        = errors.New("etf: decode target must be a non-nil pointer")
)

// DecodeError describes an error encountered while decoding a specific path in an ETF term.
type DecodeError struct {
	Path string
	Tag  byte
	Type reflect.Type
	Err  error
}

func (e *DecodeError) Error() string {
	var loc string
	if e.Path != "" {
		loc = " at " + e.Path
	}
	if e.Type != nil && e.Tag != 0 {
		return fmt.Sprintf("etf: decode error%s (tag %d -> %s): %v", loc, e.Tag, e.Type, e.Err)
	}
	if e.Type != nil {
		return fmt.Sprintf("etf: decode error%s (-> %s): %v", loc, e.Type, e.Err)
	}
	if e.Tag != 0 {
		return fmt.Sprintf("etf: decode error%s (tag %d): %v", loc, e.Tag, e.Err)
	}
	return fmt.Sprintf("etf: decode error%s: %v", loc, e.Err)
}

func (e *DecodeError) Unwrap() error {
	return e.Err
}

// InvalidTagError describes a malformed or invalid `etf` struct tag.
type InvalidTagError struct {
	Struct reflect.Type
	Field  string
	Tag    string
	Reason string
}

func (e *InvalidTagError) Error() string {
	return fmt.Sprintf("etf: invalid struct tag on %s.%s (`etf:%q`): %s", e.Struct, e.Field, e.Tag, e.Reason)
}
