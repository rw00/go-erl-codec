package etf

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

type wireReader struct {
	data []byte
	pos  int
}

func newWireReader(data []byte) *wireReader {
	return &wireReader{data: data, pos: 0}
}

func (r *wireReader) remaining() int {
	return len(r.data) - r.pos
}

func (r *wireReader) peekByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, ErrUnexpectedEOF
	}
	return r.data[r.pos], nil
}

func (r *wireReader) readByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, ErrUnexpectedEOF
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *wireReader) readBytes(n int) ([]byte, error) {
	if n < 0 || n > r.remaining() {
		return nil, ErrUnexpectedEOF
	}
	s := r.data[r.pos : r.pos+n]
	r.pos += n
	return s, nil
}

func (r *wireReader) readUint16() (uint16, error) {
	b, err := r.readBytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *wireReader) readUint32() (uint32, error) {
	b, err := r.readBytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *wireReader) readInt32() (int32, error) {
	u, err := r.readUint32()
	if err != nil {
		return 0, err
	}
	return int32(u), nil
}

// readBigInt reads SMALL_BIG_EXT or LARGE_BIG_EXT body (tag already consumed).
// Returns (i64, nil, nil) when the value fits in int64, avoiding *big.Int allocation.
func (r *wireReader) readBigInt(tag byte) (int64, *big.Int, error) {
	var n uint32
	if tag == TagSmallBig {
		b, err := r.readByte()
		if err != nil {
			return 0, nil, err
		}
		n = uint32(b)
	} else {
		u, err := r.readUint32()
		if err != nil {
			return 0, nil, err
		}
		n = u
	}

	sign, err := r.readByte()
	if err != nil {
		return 0, nil, err
	}
	if sign != 0 && sign != 1 {
		return 0, nil, ErrInvalidBignumSign
	}

	if uint64(n) > uint64(r.remaining()) {
		return 0, nil, ErrUnexpectedEOF
	}
	digits, err := r.readBytes(int(n))
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		return 0, nil, nil
	}

	// Fast path: <= 8 bytes of magnitude decoded directly into uint64/int64 without heap allocation.
	if n <= 8 {
		var mag uint64
		for i := len(digits) - 1; i >= 0; i-- {
			mag = (mag << 8) | uint64(digits[i])
		}
		if sign == 0 {
			if mag <= math.MaxInt64 {
				return int64(mag), nil, nil
			}
			return 0, new(big.Int).SetUint64(mag), nil
		}
		if mag == 0 {
			return 0, nil, nil
		}
		if mag <= math.MaxInt64 {
			return -int64(mag), nil, nil
		}
		if mag == 1<<63 {
			return math.MinInt64, nil, nil
		}
		z := new(big.Int).SetUint64(mag)
		z.Neg(z)
		return 0, z, nil
	}

	// Reverse little-endian base-256 digits to big-endian for big.Int.SetBytes.
	be := make([]byte, len(digits))
	for i := range digits {
		be[len(digits)-1-i] = digits[i]
	}
	z := new(big.Int).SetBytes(be)
	if sign == 1 && z.Sign() != 0 {
		z.Neg(z)
	}
	if z.IsInt64() {
		return z.Int64(), nil, nil
	}
	return 0, z, nil
}

func parseNewFloat(b []byte) (float64, error) {
	f := math.Float64frombits(binary.BigEndian.Uint64(b))
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, ErrNonFiniteFloat
	}
	return f, nil
}

// readFloat reads NEW_FLOAT_EXT body (tag already consumed).
func (r *wireReader) readFloat() (float64, error) {
	b, err := r.readBytes(8)
	if err != nil {
		return 0, err
	}
	return parseNewFloat(b)
}

func validateAtomBytes(b []byte) error {
	if !utf8.Valid(b) {
		return ErrInvalidUTF8
	}
	if len(b) > maxAtomUTF8Bytes || utf8.RuneCount(b) > maxAtomRunes {
		return ErrAtomTooLong
	}
	return nil
}

// readAtom reads a UTF-8 atom tag body (tag already consumed) and returns UTF-8 string.
func (r *wireReader) readAtom(tag byte) (string, error) {
	switch tag {
	case TagSmallAtomUTF8:
		n, err := r.readByte()
		if err != nil {
			return "", err
		}
		b, err := r.readBytes(int(n))
		if err != nil {
			return "", err
		}
		if err := validateAtomBytes(b); err != nil {
			return "", err
		}
		return string(b), nil

	case TagAtomUTF8:
		n, err := r.readUint16()
		if err != nil {
			return "", err
		}
		if n > maxAtomUTF8Bytes {
			return "", ErrAtomTooLong
		}
		b, err := r.readBytes(int(n))
		if err != nil {
			return "", err
		}
		if err := validateAtomBytes(b); err != nil {
			return "", err
		}
		return string(b), nil

	default:
		return "", fmt.Errorf("%w: %d", ErrUnsupportedTag, tag)
	}
}

func latin1ToUTF8(b []byte) string {
	for i, c := range b {
		if c >= 0x80 {
			var sb strings.Builder
			sb.Grow(len(b) + (len(b) - i))
			sb.Write(b[:i])
			for _, c2 := range b[i:] {
				sb.WriteRune(rune(c2))
			}
			return sb.String()
		}
	}
	return string(b)
}

// readBinary reads BINARY_EXT body (tag already consumed).
func (r *wireReader) readBinary() ([]byte, error) {
	n, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	if uint64(n) > uint64(r.remaining()) {
		return nil, ErrUnexpectedEOF
	}
	b, err := r.readBytes(int(n))
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// readBinaryString reads BINARY_EXT body (tag already consumed) directly into a Go string.
func (r *wireReader) readBinaryString() (string, error) {
	n, err := r.readUint32()
	if err != nil {
		return "", err
	}
	if uint64(n) > uint64(r.remaining()) {
		return "", ErrUnexpectedEOF
	}
	b, err := r.readBytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// readBitstring reads BIT_BINARY_EXT body (tag already consumed).
// Per Erlang ETF spec, BIT_BINARY_EXT requires Len >= 1 and Bits in 1..8.
func (r *wireReader) readBitstring() (Bitstring, error) {
	n, err := r.readUint32()
	if err != nil {
		return Bitstring{}, err
	}
	bits, err := r.readByte()
	if err != nil {
		return Bitstring{}, err
	}
	if n == 0 || bits < 1 || bits > 8 {
		return Bitstring{}, ErrInvalidBitstring
	}
	if uint64(n) > uint64(r.remaining()) {
		return Bitstring{}, ErrUnexpectedEOF
	}
	b, err := r.readBytes(int(n))
	if err != nil {
		return Bitstring{}, err
	}
	out := make([]byte, len(b))
	copy(out, b)
	// Mask unused low bits in final byte to match Erlang canonical bitstring value.
	if bits < 8 {
		out[len(out)-1] &= byte(0xFF) << (8 - bits)
	}
	return Bitstring{Data: out, Bits: bits}, nil
}

// readStringExt reads STRING_EXT body (tag already consumed) and returns a slice into r.data.
func (r *wireReader) readStringExt() ([]byte, error) {
	n, err := r.readUint16()
	if err != nil {
		return nil, err
	}
	return r.readBytes(int(n))
}

// readListHeader reads LIST_EXT element count (tag already consumed) and verifies minimum remaining bytes.
func (r *wireReader) readListHeader() (uint32, error) {
	n, err := r.readUint32()
	if err != nil {
		return 0, err
	}
	if uint64(n)+1 > uint64(r.remaining()) {
		return 0, ErrUnexpectedEOF
	}
	return n, nil
}

// readMapHeader reads MAP_EXT arity (tag already consumed) and verifies minimum remaining bytes.
func (r *wireReader) readMapHeader() (uint32, error) {
	arity, err := r.readUint32()
	if err != nil {
		return 0, err
	}
	if uint64(arity)*2 > uint64(r.remaining()) {
		return 0, ErrUnexpectedEOF
	}
	return arity, nil
}

// readTupleHeader reads SMALL_TUPLE_EXT or LARGE_TUPLE_EXT arity (tag already consumed)
// and verifies minimum remaining bytes.
func (r *wireReader) readTupleHeader(tag byte) (uint32, error) {
	var arity uint32
	if tag == TagSmallTuple {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		arity = uint32(b)
	} else {
		u, err := r.readUint32()
		if err != nil {
			return 0, err
		}
		arity = u
	}
	if uint64(arity) > uint64(r.remaining()) {
		return 0, ErrUnexpectedEOF
	}
	return arity, nil
}

func (r *wireReader) readCharlistBody(n uint32, depth, maxDepth int, dst *strings.Builder) error {
	if depth+1 > maxDepth {
		return ErrMaxDepthExceeded
	}
	if dst != nil {
		dst.Grow(cappedCap(int(n)))
	}
	for i := uint32(0); i < n; i++ {
		i64, bi, tag, err := r.readAnyInteger()
		if err != nil {
			return fmt.Errorf("element %d (tag %d): %w", i, tag, err)
		}
		if bi != nil || i64 < 0 || i64 > utf8.MaxRune || !utf8.ValidRune(rune(i64)) {
			return fmt.Errorf("%w: element %d is not a valid Unicode code point", ErrTypeMismatch, i)
		}
		if dst != nil {
			dst.WriteRune(rune(i64))
		}
	}
	tailTag, err := r.readByte()
	if err != nil {
		return err
	}
	if tailTag != TagNil {
		return ErrImproperList
	}
	return nil
}

func (r *wireReader) readAnyInteger() (int64, *big.Int, byte, error) {
	tag, err := r.readByte()
	if err != nil {
		return 0, nil, 0, err
	}
	switch tag {
	case TagSmallInt:
		b, err := r.readByte()
		return int64(b), nil, tag, err
	case TagInt:
		i, err := r.readInt32()
		return int64(i), nil, tag, err
	case TagSmallBig, TagLargeBig:
		i64, bi, err := r.readBigInt(tag)
		return i64, bi, tag, err
	default:
		return 0, nil, tag, fmt.Errorf("%w: expected integer tag, got %d", ErrTypeMismatch, tag)
	}
}

// skipAndValidateTerm advances past one complete ETF term while enforcing spec
// (including atom values for boolean, byte alignment for binary, Unicode code points
// for charlist, and recursive element validation for list(T)).
func (r *wireReader) skipAndValidateTerm(spec *typeSpec, depth, maxDepth int) error {
	if depth > maxDepth {
		return ErrMaxDepthExceeded
	}
	if spec == nil || spec.kind == valAny {
		return r.skipTerm(depth, maxDepth)
	}
	tag, err := r.peekByte()
	if err != nil {
		return err
	}
	if err := validateTagForSpec(tag, spec); err != nil {
		return err
	}

	switch spec.kind {
	case valBoolean:
		_, _ = r.readByte()
		s, err := r.readAtom(tag)
		if err != nil {
			return err
		}
		if s != "true" && s != "false" {
			return fmt.Errorf("%w: expected boolean atom (true/false), got atom %q", ErrTypeMismatch, s)
		}
		return nil

	case valBinary:
		if tag == TagBitBinary {
			_, _ = r.readByte()
			bs, err := r.readBitstring()
			if err != nil {
				return err
			}
			if len(bs.Data) > 0 && bs.Bits != 8 {
				return fmt.Errorf("%w: non-byte-aligned bitstring (bits=%d) does not match binary", ErrTypeMismatch, bs.Bits)
			}
			return nil
		}
		return r.skipTerm(depth, maxDepth)

	case valCharlist:
		if tag == TagList {
			_, _ = r.readByte()
			n, err := r.readListHeader()
			if err != nil {
				return err
			}
			return r.readCharlistBody(n, depth, maxDepth, nil)
		}
		return r.skipTerm(depth, maxDepth)

	case valDateTime:
		if depth+2 > maxDepth {
			return ErrMaxDepthExceeded
		}
		_, err := readDateTimeWire(r)
		return err

	case valList:
		switch tag {
		case TagNil:
			_, _ = r.readByte()
			return nil
		case TagString:
			_, _ = r.readByte()
			if _, err := r.readStringExt(); err != nil {
				return err
			}
			if spec.elem != nil && spec.elem.kind != valAny && spec.elem.kind != valInteger && spec.elem.kind != valNumber {
				return fmt.Errorf("%w: list of bytes (STRING_EXT) does not match %s", ErrTypeMismatch, spec.rawText)
			}
			return nil
		case TagList:
			_, _ = r.readByte()
			n, err := r.readListHeader()
			if err != nil {
				return err
			}
			for i := uint32(0); i < n; i++ {
				if err := r.skipAndValidateTerm(spec.elem, depth+1, maxDepth); err != nil {
					return err
				}
			}
			tailTag, err := r.peekByte()
			if err != nil {
				return err
			}
			if tailTag == TagNil {
				_, _ = r.readByte()
				return nil
			}
			if n == 0 {
				return ErrImproperList
			}
			return r.skipTerm(depth+1, maxDepth)
		}
	}

	return r.skipTerm(depth, maxDepth)
}

// termSource abstracts byte-level ETF framing over both in-memory slices (*wireReader)
// and streaming readers (*streamTermSource) so wire tag framing lives in one place.
type termSource interface {
	readTermTag() (byte, error)
	readTermBytes(n uint32) ([]byte, error)
	checkTermMinBytes(n uint64) error
}

func (r *wireReader) readTermTag() (byte, error) {
	return r.readByte()
}

func (r *wireReader) readTermBytes(n uint32) ([]byte, error) {
	if uint64(n) > uint64(r.remaining()) {
		return nil, ErrUnexpectedEOF
	}
	return r.readBytes(int(n))
}

func (r *wireReader) checkTermMinBytes(n uint64) error {
	if n > uint64(r.remaining()) {
		return ErrUnexpectedEOF
	}
	return nil
}

// skipTerm advances past one complete ETF term, enforcing maxDepth and validating wire constraints.
func (r *wireReader) skipTerm(depth, maxDepth int) error {
	return walkTerm(r, depth, maxDepth, true, false)
}

// skipIgnoredTerm advances past an unused/ignored ETF term (e.g. an unknown struct field),
// including unsupported and legacy ETF wire tags.
func (r *wireReader) skipIgnoredTerm(depth, maxDepth int) error {
	return walkTerm(r, depth, maxDepth, true, true)
}

func walkTerm(src termSource, depth, maxDepth int, validate, allowLegacy bool) error {
	if depth > maxDepth {
		return ErrMaxDepthExceeded
	}
	tag, err := src.readTermTag()
	if err != nil {
		return err
	}
	return walkTermBody(src, tag, depth, maxDepth, validate, allowLegacy)
}

func walkTermBody(src termSource, tag byte, depth, maxDepth int, validate, allowLegacy bool) error {
	if depth > maxDepth {
		return ErrMaxDepthExceeded
	}
	switch tag {
	case TagSmallInt:
		_, err := src.readTermBytes(1)
		return err
	case TagInt:
		_, err := src.readTermBytes(4)
		return err
	case TagNewFloat:
		b, err := src.readTermBytes(8)
		if err != nil {
			return err
		}
		if validate {
			_, err = parseNewFloat(b)
			return err
		}
		return nil
	case TagSmallAtomUTF8:
		h, err := src.readTermBytes(1)
		if err != nil {
			return err
		}
		b, err := src.readTermBytes(uint32(h[0]))
		if err != nil {
			return err
		}
		if validate {
			return validateAtomBytes(b)
		}
		return nil
	case TagAtomUTF8:
		h, err := src.readTermBytes(2)
		if err != nil {
			return err
		}
		n := binary.BigEndian.Uint16(h)
		if n > maxAtomUTF8Bytes {
			return ErrAtomTooLong
		}
		b, err := src.readTermBytes(uint32(n))
		if err != nil {
			return err
		}
		if validate {
			return validateAtomBytes(b)
		}
		return nil
	case TagSmallBig:
		h, err := src.readTermBytes(2) // n(1) + sign(1)
		if err != nil {
			return err
		}
		if validate && h[1] != 0 && h[1] != 1 {
			return ErrInvalidBignumSign
		}
		_, err = src.readTermBytes(uint32(h[0]))
		return err
	case TagLargeBig:
		h, err := src.readTermBytes(5) // n(4) + sign(1)
		if err != nil {
			return err
		}
		if validate && h[4] != 0 && h[4] != 1 {
			return ErrInvalidBignumSign
		}
		_, err = src.readTermBytes(binary.BigEndian.Uint32(h[:4]))
		return err
	case TagBinary:
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		_, err = src.readTermBytes(binary.BigEndian.Uint32(h))
		return err
	case TagBitBinary:
		h, err := src.readTermBytes(5) // n(4) + bits(1)
		if err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(h[:4])
		if validate && (n == 0 || h[4] < 1 || h[4] > 8) {
			return ErrInvalidBitstring
		}
		_, err = src.readTermBytes(n)
		return err
	case TagNil:
		return nil
	case TagString:
		h, err := src.readTermBytes(2)
		if err != nil {
			return err
		}
		_, err = src.readTermBytes(uint32(binary.BigEndian.Uint16(h)))
		return err
	case TagList:
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(h)
		if err := src.checkTermMinBytes(uint64(n) + 1); err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return walkTerm(src, depth+1, maxDepth, validate, allowLegacy) // tail
	case TagMap:
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		arity := binary.BigEndian.Uint32(h)
		if err := src.checkTermMinBytes(uint64(arity) * 2); err != nil {
			return err
		}
		for i := uint32(0); i < arity; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return nil
	case TagSmallTuple:
		h, err := src.readTermBytes(1)
		if err != nil {
			return err
		}
		arity := uint32(h[0])
		if err := src.checkTermMinBytes(uint64(arity)); err != nil {
			return err
		}
		for i := uint32(0); i < arity; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return nil
	case TagLargeTuple:
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		arity := binary.BigEndian.Uint32(h)
		if err := src.checkTermMinBytes(uint64(arity)); err != nil {
			return err
		}
		for i := uint32(0); i < arity; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return nil
	default:
		return walkUnsupportedTermBody(src, tag, depth, maxDepth, validate, allowLegacy)
	}
}

// walkUnsupportedTermBody skips ETF wire tags that this codec does not materialize
// (PIDs, ports, references, funs, exports, local wrappers, and legacy atom/float tags)
// so ignored struct fields and stream framing can advance past them safely.
func walkUnsupportedTermBody(src termSource, tag byte, depth, maxDepth int, validate, allowLegacy bool) error {
	switch tag {
	case 82: // ATOM_CACHE_REF: Index(1)
		_, err := src.readTermBytes(1)
		return err
	case 88, 120: // NEW_PID_EXT: Node + ID(4)+Serial(4)+Creation(4); V4_PORT_EXT: Node + ID(8)+Creation(4)
		if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
			return err
		}
		_, err := src.readTermBytes(12)
		return err
	case 89: // NEW_PORT_EXT: Node + ID(4)+Creation(4)
		if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
			return err
		}
		_, err := src.readTermBytes(8)
		return err
	case 103: // PID_EXT: Node + ID(4)+Serial(4)+Creation(1)
		if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
			return err
		}
		_, err := src.readTermBytes(9)
		return err
	case 101, 102: // REFERENCE_EXT / PORT_EXT: Node + ID(4)+Creation(1)
		if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
			return err
		}
		_, err := src.readTermBytes(5)
		return err
	case 90, 114: // NEWER_REFERENCE_EXT (Creation=4) / NEW_REFERENCE_EXT (Creation=1): Len(2) + Node + Creation + 4*Len
		h, err := src.readTermBytes(2)
		if err != nil {
			return err
		}
		n := uint32(binary.BigEndian.Uint16(h))
		if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
			return err
		}
		creationLen := uint32(4)
		if tag == 114 {
			creationLen = 1
		}
		_, err = src.readTermBytes(creationLen + 4*n)
		return err
	case 112: // NEW_FUN_EXT: Size(4, includes itself; min 29) + body
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(h)
		if size < 29 {
			return ErrUnexpectedEOF
		}
		_, err = src.readTermBytes(size - 4)
		return err
	case 113: // EXPORT_EXT: Module + Function + Arity
		for i := 0; i < 3; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return nil
	case 117: // FUN_EXT: NumFree(4) + Pid + Module + Index + Uniq + NumFree FreeVars
		h, err := src.readTermBytes(4)
		if err != nil {
			return err
		}
		total := uint64(binary.BigEndian.Uint32(h)) + 4
		if err := src.checkTermMinBytes(total); err != nil {
			return err
		}
		for i := uint64(0); i < total; i++ {
			if err := walkTerm(src, depth+1, maxDepth, validate, allowLegacy); err != nil {
				return err
			}
		}
		return nil
	case 121: // LOCAL_EXT: Hash(4) + Term
		if _, err := src.readTermBytes(4); err != nil {
			return err
		}
		return walkTerm(src, depth+1, maxDepth, validate, allowLegacy)
	case 99: // Legacy FLOAT_EXT: 31 bytes
		if !allowLegacy {
			return fmt.Errorf("%w: %d", ErrUnsupportedTag, tag)
		}
		_, err := src.readTermBytes(31)
		return err
	case 100: // Legacy ATOM_EXT: Len(2) + bytes
		if !allowLegacy {
			return fmt.Errorf("%w: %d", ErrUnsupportedTag, tag)
		}
		h, err := src.readTermBytes(2)
		if err != nil {
			return err
		}
		_, err = src.readTermBytes(uint32(binary.BigEndian.Uint16(h)))
		return err
	case 115: // Legacy SMALL_ATOM_EXT: Len(1) + bytes
		if !allowLegacy {
			return fmt.Errorf("%w: %d", ErrUnsupportedTag, tag)
		}
		h, err := src.readTermBytes(1)
		if err != nil {
			return err
		}
		_, err = src.readTermBytes(uint32(h[0]))
		return err
	default:
		return fmt.Errorf("%w: %d", ErrUnsupportedTag, tag)
	}
}

func inflateZlibTerm(r io.Reader, uncompSize uint32, maxUncompressedSize int64) ([]byte, error) {
	if int64(uncompSize) > maxUncompressedSize {
		return nil, ErrMaxSizeExceeded
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("etf: invalid compressed payload: %w", err)
	}
	out, readErr := io.ReadAll(io.LimitReader(zr, int64(uncompSize)+1))
	closeErr := zr.Close()
	if readErr != nil {
		return nil, fmt.Errorf("etf: zlib decompress error: %w", readErr)
	}
	if int64(len(out)) > int64(uncompSize) {
		return nil, ErrMaxSizeExceeded
	}
	if closeErr != nil {
		return nil, fmt.Errorf("etf: zlib close error: %w", closeErr)
	}
	if uint32(len(out)) != uncompSize {
		return nil, fmt.Errorf("etf: decompressed size %d does not match header %d", len(out), uncompSize)
	}
	return out, nil
}

// unwrapETFPayload strips the leading VersionMagic (131) and transparently inflates
// zlib-compressed terms (tag 80) while enforcing maxUncompressedSize.
func unwrapETFPayload(data []byte, maxUncompressedSize int64) ([]byte, error) {
	if len(data) < 2 {
		if len(data) == 0 {
			return nil, ErrUnexpectedEOF
		}
		if data[0] != VersionMagic {
			return nil, ErrInvalidVersion
		}
		return nil, ErrUnexpectedEOF
	}
	if data[0] != VersionMagic {
		return nil, ErrInvalidVersion
	}
	if data[1] != TagCompressed {
		return data[1:], nil
	}
	if len(data) < 6 {
		return nil, ErrUnexpectedEOF
	}
	br := bytes.NewReader(data[6:])
	out, err := inflateZlibTerm(br, binary.BigEndian.Uint32(data[2:6]), maxUncompressedSize)
	if err != nil {
		return nil, err
	}
	if br.Len() != 0 {
		return nil, ErrTrailingBytes
	}
	return out, nil
}

type streamTermSource struct {
	br       *bufio.Reader
	dst      *bytes.Buffer
	maxBytes int64
}

func (s *streamTermSource) readTermTag() (byte, error) {
	if int64(s.dst.Len()) >= s.maxBytes {
		return 0, ErrMaxSizeExceeded
	}
	tag, err := s.br.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, ErrUnexpectedEOF
		}
		return 0, err
	}
	s.dst.WriteByte(tag)
	return tag, nil
}

func (s *streamTermSource) readTermBytes(n uint32) ([]byte, error) {
	if int64(n) > s.maxBytes-int64(s.dst.Len()) {
		return nil, ErrMaxSizeExceeded
	}
	start := s.dst.Len()
	if _, err := io.CopyN(s.dst, s.br, int64(n)); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, ErrUnexpectedEOF
		}
		return nil, err
	}
	return s.dst.Bytes()[start:], nil
}

func (s *streamTermSource) checkTermMinBytes(n uint64) error {
	rem := s.maxBytes - int64(s.dst.Len())
	if rem < 0 || n > uint64(rem) {
		return ErrMaxSizeExceeded
	}
	return nil
}

// readStandaloneTermFromReader reads exactly one complete standalone ETF term
// (starting with VersionMagic 131) from br without consuming bytes of subsequent terms.
func readStandaloneTermFromReader(br *bufio.Reader, cfg decodeConfig) ([]byte, error) {
	ver, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if ver != VersionMagic {
		return nil, ErrInvalidVersion
	}

	tag, err := br.ReadByte()
	if err != nil {
		if err == io.EOF {
			return nil, ErrUnexpectedEOF
		}
		return nil, err
	}

	if tag == TagCompressed {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil, ErrUnexpectedEOF
			}
			return nil, err
		}
		return inflateZlibTerm(br, binary.BigEndian.Uint32(hdr[:]), cfg.maxUncompressedSize)
	}

	if cfg.maxUncompressedSize < 1 {
		return nil, ErrMaxSizeExceeded
	}
	var buf bytes.Buffer
	buf.WriteByte(tag)
	src := streamTermSource{br: br, dst: &buf, maxBytes: cfg.maxUncompressedSize}
	if err := walkTermBody(&src, tag, 0, cfg.maxDepth, false, true); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
