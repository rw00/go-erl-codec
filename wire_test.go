package etf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestWireIntegersAndBignums(t *testing.T) {
	bigPos, _ := new(big.Int).SetString("1234567890123456789012345678901234567890", 10)
	bigNeg, _ := new(big.Int).SetString("-9876543210987654321098765432109876543210", 10)
	// > 255 bytes -> LARGE_BIG_EXT
	huge := new(big.Int).Lsh(big.NewInt(1), 2200)
	hugeNeg := new(big.Int).Neg(huge)

	cases := []struct {
		name    string
		in      any
		wantTag byte
	}{
		{"zero", int64(0), TagSmallInt},
		{"max_small", int64(255), TagSmallInt},
		{"min_neg_int32", int64(math.MinInt32), TagInt},
		{"max_pos_int32", int64(math.MaxInt32), TagInt},
		{"neg_one", int64(-1), TagInt},
		{"int64_above_int32", int64(math.MaxInt32) + 1, TagSmallBig},
		{"min_int64", int64(math.MinInt64), TagSmallBig},
		{"max_int64", int64(math.MaxInt64), TagSmallBig},
		{"max_uint64", uint64(math.MaxUint64), TagSmallBig},
		{"big_pos", bigPos, TagSmallBig},
		{"big_neg", bigNeg, TagSmallBig},
		{"large_big_pos", huge, TagLargeBig},
		{"large_big_neg", hugeNeg, TagLargeBig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			if raw[0] != VersionMagic {
				t.Fatalf("expected version magic 131, got %d", raw[0])
			}
			if raw[1] != tc.wantTag {
				t.Fatalf("expected tag %d, got %d", tc.wantTag, raw[1])
			}

			var out any
			if err := Unmarshal(raw, &out); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if !Equal(tc.in, out) {
				t.Fatalf("round-trip mismatch: in=%T(%v), out=%T(%v)", tc.in, tc.in, out, out)
			}
		})
	}
}

func TestWireFloatsAndNonFinite(t *testing.T) {
	// Reject NaN and Inf on Marshal.
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := Marshal(bad); !errors.Is(err, ErrNonFiniteFloat) {
			t.Fatalf("expected ErrNonFiniteFloat for %v, got %v", bad, err)
		}
	}

	// Reject NaN on Unmarshal.
	nanBytes := append([]byte{VersionMagic, TagNewFloat}, binary.BigEndian.AppendUint64(nil, math.Float64bits(math.NaN()))...)
	var f float64
	if err := Unmarshal(nanBytes, &f); !errors.Is(err, ErrNonFiniteFloat) {
		t.Fatalf("expected ErrNonFiniteFloat on decode, got %v", err)
	}

	// Removed legacy FLOAT_EXT (tag 99) must be rejected with ErrUnsupportedTag.
	legacy := make([]byte, 33)
	legacy[0] = VersionMagic
	legacy[1] = 99
	copy(legacy[2:], "3.14159000000000010466e+00")
	if err := Unmarshal(legacy, &f); !errors.Is(err, ErrUnsupportedTag) {
		t.Fatalf("expected ErrUnsupportedTag for legacy FLOAT_EXT, got %v", err)
	}
}

func TestWireAtomsUTF8AndLengthValidation(t *testing.T) {
	// 255 multibyte runes (>255 bytes -> ATOM_UTF8_EXT tag 118).
	longUTF8 := Atom(strings.Repeat("é", 255))
	raw, err := Marshal(longUTF8)
	if err != nil {
		t.Fatalf("Marshal long UTF-8 atom failed: %v", err)
	}
	if raw[1] != TagAtomUTF8 {
		t.Fatalf("expected ATOM_UTF8_EXT (118), got %d", raw[1])
	}
	var got Atom
	if err := Unmarshal(raw, &got); err != nil || got != longUTF8 {
		t.Fatalf("Unmarshal long UTF-8 atom failed: got=%q err=%v", got, err)
	}

	// 255 4-byte runes (1020 bytes, max valid UTF-8 atom byte length).
	maxBytesUTF8 := Atom(strings.Repeat("🚀", 255))
	rawMax, err := Marshal(maxBytesUTF8)
	if err != nil {
		t.Fatalf("Marshal 255x4-byte UTF-8 atom failed: %v", err)
	}
	if err := Unmarshal(rawMax, &got); err != nil || got != maxBytesUTF8 {
		t.Fatalf("Unmarshal 255x4-byte UTF-8 atom failed: err=%v", err)
	}

	// 256 runes must fail ErrAtomTooLong on Marshal.
	for _, tooLong := range []Atom{
		Atom(strings.Repeat("a", 256)),
		Atom(strings.Repeat("é", 256)),
		Atom(strings.Repeat("🚀", 256)),
	} {
		if _, err := Marshal(tooLong); !errors.Is(err, ErrAtomTooLong) {
			t.Fatalf("Marshal(%d bytes): expected ErrAtomTooLong, got %v", len(tooLong), err)
		}
	}

	// Wire ATOM_UTF8_EXT exceeding 255 runes (256 ASCII bytes, 256 2-byte runes, >1020 bytes) must fail ErrAtomTooLong
	// across Unmarshal, RawTerm (skipTerm), and streaming Decoder.
	makeAtomUTF8 := func(s string) []byte {
		out := []byte{VersionMagic, TagAtomUTF8, byte(len(s) >> 8), byte(len(s))}
		return append(out, s...)
	}
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"256_ascii_runes", makeAtomUTF8(strings.Repeat("a", 256))},
		{"256_2byte_runes", makeAtomUTF8(strings.Repeat("é", 256))},
		{"256_4byte_runes", makeAtomUTF8(strings.Repeat("🚀", 256))},
		{"header_over_1020", []byte{VersionMagic, TagAtomUTF8, 0x04, 0x00}}, // claims 1024 bytes
	} {
		var dst Atom
		if err := Unmarshal(tc.wire, &dst); !errors.Is(err, ErrAtomTooLong) {
			t.Fatalf("Unmarshal %s: expected ErrAtomTooLong, got %v", tc.name, err)
		}
		var rawDst RawTerm
		if err := Unmarshal(tc.wire, &rawDst); !errors.Is(err, ErrAtomTooLong) {
			t.Fatalf("Unmarshal RawTerm %s: expected ErrAtomTooLong, got %v", tc.name, err)
		}
		if err := NewDecoder(bytes.NewReader(tc.wire)).Decode(&dst); !errors.Is(err, ErrAtomTooLong) {
			t.Fatalf("Decoder %s: expected ErrAtomTooLong, got %v", tc.name, err)
		}
	}

	// Removed legacy ATOM_EXT (100) and SMALL_ATOM_EXT (115) must be rejected with ErrUnsupportedTag.
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"legacy_atom_ext", []byte{VersionMagic, 100, 0, 3, 'c', 'a', 0xE9}},
		{"legacy_small_atom_ext", []byte{VersionMagic, 115, 2, 0xFC, 'b'}},
	} {
		if err := Unmarshal(tc.wire, &got); !errors.Is(err, ErrUnsupportedTag) {
			t.Fatalf("%s: expected ErrUnsupportedTag, got %v", tc.name, err)
		}
		var rawDst RawTerm
		if err := Unmarshal(tc.wire, &rawDst); !errors.Is(err, ErrUnsupportedTag) {
			t.Fatalf("%s RawTerm: expected ErrUnsupportedTag, got %v", tc.name, err)
		}
	}
}

func TestWireCompressionAndLimits(t *testing.T) {
	data := Binary(strings.Repeat("hello erlang etf compression! ", 100))
	comp, err := Marshal(data, WithCompression(zlib.BestCompression))
	if err != nil {
		t.Fatalf("compressed Marshal failed: %v", err)
	}
	if comp[0] != VersionMagic || comp[1] != TagCompressed {
		t.Fatalf("expected compressed header [131, 80], got [%d, %d]", comp[0], comp[1])
	}

	var got Binary
	if err := Unmarshal(comp, &got); err != nil {
		t.Fatalf("compressed Unmarshal failed: %v", err)
	}
	if got != data {
		t.Fatalf("compressed round-trip mismatch")
	}

	// Exceeding MaxUncompressedSize fails with ErrMaxSizeExceeded.
	if err := Unmarshal(comp, &got, WithMaxUncompressedSize(32)); !errors.Is(err, ErrMaxSizeExceeded) {
		t.Fatalf("expected ErrMaxSizeExceeded, got %v", err)
	}

	// Trailing bytes after compressed payload must fail with ErrTrailingBytes.
	withTrailing := append(append([]byte(nil), comp...), 0x42)
	if err := Unmarshal(withTrailing, &got); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("expected ErrTrailingBytes for compressed trailing byte, got %v", err)
	}

	// Corrupt Adler-32 checksum in compressed payload must fail.
	corrupt := append([]byte(nil), comp...)
	corrupt[len(corrupt)-1] ^= 0xFF
	if err := Unmarshal(corrupt, &got); err == nil {
		t.Fatalf("expected error for corrupt compressed checksum")
	}
}

func TestWireSafetyLimitsAndMalformedInputs(t *testing.T) {
	var out any

	// Invalid version byte.
	if err := Unmarshal([]byte{130, TagNil}, &out); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("expected ErrInvalidVersion, got %v", err)
	}

	// Trailing bytes.
	if err := Unmarshal([]byte{VersionMagic, TagSmallInt, 1, TagNil}, &out); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("expected ErrTrailingBytes, got %v", err)
	}

	// Malicious 4GB binary length claim -> ErrUnexpectedEOF without OOM.
	maliciousBin := []byte{VersionMagic, TagBinary, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2}
	if err := Unmarshal(maliciousBin, &out); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF, got %v", err)
	}

	// Malicious 4GB map arity -> ErrUnexpectedEOF without OOM.
	maliciousMap := []byte{VersionMagic, TagMap, 0xFF, 0xFF, 0xFF, 0xFF}
	if err := Unmarshal(maliciousMap, &out); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF, got %v", err)
	}

	// MaxDepth exceeded.
	var deep bytes.Buffer
	deep.WriteByte(VersionMagic)
	for i := 0; i < 20; i++ {
		deep.Write([]byte{TagList, 0, 0, 0, 1})
	}
	deep.WriteByte(TagSmallInt)
	deep.WriteByte(1)
	for i := 0; i < 20; i++ {
		deep.WriteByte(TagNil)
	}
	if err := Unmarshal(deep.Bytes(), &out, WithMaxDecodeDepth(10)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestStreamingEncoderDecoder(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	terms := []any{
		int64(42),
		Atom("ok"),
		Binary("payload"),
		Map{{Key: Atom("a"), Value: int64(1)}},
	}
	for _, term := range terms {
		if err := enc.Encode(term); err != nil {
			t.Fatalf("Encode failed: %v", err)
		}
	}

	// Also append a compressed term to the same stream!
	compEnc := NewEncoder(&buf, WithCompression(zlib.DefaultCompression))
	if err := compEnc.Encode(Binary("compressed-in-stream")); err != nil {
		t.Fatalf("compressed Encode failed: %v", err)
	}

	dec := NewDecoder(&buf)
	for i, want := range append(terms, Binary("compressed-in-stream")) {
		var got any
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("Decode[%d] failed: %v", i, err)
		}
		if !Equal(want, got) {
			t.Fatalf("stream item %d mismatch: want=%#v got=%#v", i, want, got)
		}
	}

	// EOF at stream boundary.
	var eofVal any
	if err := dec.Decode(&eofVal); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF at end of stream, got %v", err)
	}

	// Truncated term returns ErrUnexpectedEOF.
	truncDec := NewDecoder(bytes.NewReader([]byte{VersionMagic, TagBinary, 0, 0, 0, 10, 'a', 'b'}))
	if err := truncDec.Decode(&eofVal); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF for truncated stream, got %v", err)
	}

	// Invalid version byte returns ErrInvalidVersion.
	badVerDec := NewDecoder(bytes.NewReader([]byte{130, TagNil}))
	if err := badVerDec.Decode(&eofVal); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("expected ErrInvalidVersion, got %v", err)
	}

	// Streaming Decoder enforces MaxUncompressedSize against malicious TagBinary / TagList / TagMap claims.
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"huge_binary", []byte{VersionMagic, TagBinary, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"huge_list", []byte{VersionMagic, TagList, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"huge_map", []byte{VersionMagic, TagMap, 0xFF, 0xFF, 0xFF, 0xFF}},
	} {
		d := NewDecoder(bytes.NewReader(tc.wire), WithMaxUncompressedSize(1024))
		var v any
		if err := d.Decode(&v); !errors.Is(err, ErrMaxSizeExceeded) {
			t.Fatalf("%s: expected ErrMaxSizeExceeded, got %v", tc.name, err)
		}
	}

	// Streaming Decoder returns ErrMaxSizeExceeded when compressed payload exceeds header uncompSize.
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	_, _ = zw.Write([]byte{TagBinary, 0, 0, 0, 10, '0', '1', '2', '3', '4', '5', '6', '7', '8', '9'})
	_ = zw.Close()
	lyingComp := append([]byte{VersionMagic, TagCompressed, 0, 0, 0, 4}, zbuf.Bytes()...)
	var compDst any
	if err := NewDecoder(bytes.NewReader(lyingComp)).Decode(&compDst); !errors.Is(err, ErrMaxSizeExceeded) {
		t.Fatalf("expected ErrMaxSizeExceeded for lying compressed header in Decoder, got %v", err)
	}
}

func TestEncodeMaxDepthAndMarshalerDepth(t *testing.T) {
	// Nested slices exceeding WithMaxDepth.
	deep := []any{[]any{[]any{[]any{1}}}}
	if _, err := Marshal(deep, WithMaxDepth(2)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded on Marshal, got %v", err)
	}
	if _, err := Marshal(deep, WithMaxDepth(10)); err != nil {
		t.Fatalf("unexpected error with sufficient depth: %v", err)
	}

	// Nested RawTerm (Marshaler) must account for current encoding depth.
	innerRaw, err := Marshal([]any{[]any{1}})
	if err != nil {
		t.Fatal(err)
	}
	outer := []any{[]any{RawTerm(innerRaw)}} // total depth 4
	if _, err := Marshal(outer, WithMaxDepth(3)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded for nested RawTerm, got %v", err)
	}
}

func TestEncoderBufferReuseAndOversizedRelease(t *testing.T) {
	var out bytes.Buffer
	enc := NewEncoder(&out)

	if err := enc.Encode(Binary("small-1")); err != nil {
		t.Fatal(err)
	}
	if cap(enc.buf.buf) == 0 {
		t.Fatal("expected Encoder to retain small buffer capacity for reuse")
	}
	if err := enc.Encode(Binary("small-2")); err != nil {
		t.Fatal(err)
	}

	large := Binary(strings.Repeat("x", maxPreallocBytes+1024))
	if err := enc.Encode(large); err != nil {
		t.Fatal(err)
	}
	if enc.buf.buf != nil {
		t.Fatalf("expected oversized Encoder buffer (cap=%d) to be released", cap(enc.buf.buf))
	}

	if err := enc.Encode(Atom("after-large")); err != nil {
		t.Fatal(err)
	}

	dec := NewDecoder(&out)
	for i, want := range []any{Binary("small-1"), Binary("small-2"), large, Atom("after-large")} {
		var got any
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("Decode[%d] failed: %v", i, err)
		}
		if !Equal(want, got) {
			t.Fatalf("Decode[%d] mismatch", i)
		}
	}
}
