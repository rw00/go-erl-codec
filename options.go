package etf

import "compress/zlib"

const (
	defaultMaxDepth            = 1000
	defaultMaxUncompressedSize = 64 << 20 // 64 MiB
	noCompression              = -100
)

type encodeConfig struct {
	compressionLevel int
	nilAtom          Atom
	nilAtomErr       error
	maxDepth         int
}

func defaultEncodeConfig() encodeConfig {
	return encodeConfig{
		compressionLevel: noCompression,
		nilAtom:          Atom("undefined"),
		maxDepth:         defaultMaxDepth,
	}
}

// EncodeOption configures ETF encoding behavior.
type EncodeOption func(*encodeConfig)

// WithCompression enables zlib compression (ETF tag 80) at the given zlib level
// (zlib.NoCompression..zlib.BestCompression or zlib.DefaultCompression).
func WithCompression(level int) EncodeOption {
	return func(c *encodeConfig) {
		if level < zlib.HuffmanOnly || level > zlib.BestCompression {
			level = zlib.DefaultCompression
		}
		c.compressionLevel = level
	}
}

// WithNilAtom sets the Erlang atom emitted for Go nil pointers and nil interfaces.
// Defaults to Atom("undefined").
func WithNilAtom(atom Atom) EncodeOption {
	err := validateAtom(string(atom))
	return func(c *encodeConfig) {
		c.nilAtom = atom
		c.nilAtomErr = err
	}
}

// WithMaxDepth sets the maximum allowed nesting depth during encoding.
// Defaults to 1000.
func WithMaxDepth(n int) EncodeOption {
	return func(c *encodeConfig) {
		if n > 0 {
			c.maxDepth = n
		}
	}
}

type decodeConfig struct {
	nilAtom                Atom
	nilAtomErr             error
	strictTypes            bool
	disallowUnknownFields  bool
	rejectDuplicateMapKeys bool
	maxDepth               int
	maxUncompressedSize    int64
}

func defaultDecodeConfig() decodeConfig {
	return decodeConfig{
		nilAtom:                Atom("undefined"),
		strictTypes:            false,
		disallowUnknownFields:  false,
		rejectDuplicateMapKeys: true,
		maxDepth:               defaultMaxDepth,
		maxUncompressedSize:    defaultMaxUncompressedSize,
	}
}

// DecodeOption configures ETF decoding behavior.
type DecodeOption func(*decodeConfig)

// WithDecodeNilAtom sets the Erlang atom recognized as Go nil for pointer targets.
// Defaults to Atom("undefined").
func WithDecodeNilAtom(atom Atom) DecodeOption {
	err := validateAtom(string(atom))
	return func(c *decodeConfig) {
		c.nilAtom = atom
		c.nilAtomErr = err
	}
}

// WithStrictTypes disables cross-type coercion when a struct field omits an explicit
// Erlang value type or when decoding standalone values.
func WithStrictTypes() DecodeOption {
	return func(c *decodeConfig) {
		c.strictTypes = true
	}
}

// WithDisallowUnknownFields causes decoding into a struct to return an error
// when the incoming Erlang map contains keys that do not match any struct field.
func WithDisallowUnknownFields() DecodeOption {
	return func(c *decodeConfig) {
		c.disallowUnknownFields = true
	}
}

// WithAllowDuplicateMapKeys disables duplicate key rejection when decoding maps
// (last value wins). By default duplicate keys return ErrDuplicateMapKey.
func WithAllowDuplicateMapKeys() DecodeOption {
	return func(c *decodeConfig) {
		c.rejectDuplicateMapKeys = false
	}
}

// WithMaxDecodeDepth sets the maximum allowed nesting depth during decoding.
// Defaults to 1000.
func WithMaxDecodeDepth(n int) DecodeOption {
	return func(c *decodeConfig) {
		if n > 0 {
			c.maxDepth = n
		}
	}
}

// WithMaxUncompressedSize sets the maximum allowed uncompressed payload size (in bytes)
// for zlib-compressed ETF terms (tag 80) and streaming Decoder term buffering. Defaults to 64 MiB.
func WithMaxUncompressedSize(bytes int64) DecodeOption {
	return func(c *decodeConfig) {
		if bytes > 0 {
			c.maxUncompressedSize = bytes
		}
	}
}
