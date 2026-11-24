package etf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"unicode/utf8"
)

type wireWriter struct {
	buf []byte
}

func (w *wireWriter) reset() {
	w.buf = w.buf[:0]
}

func (w *wireWriter) bytes() []byte {
	return w.buf
}

func (w *wireWriter) writeByte(b byte) {
	w.buf = append(w.buf, b)
}

func (w *wireWriter) writeUint32(v uint32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, v)
}

func (w *wireWriter) writeTupleHeader(arity int) error {
	if arity < 0 || uint64(arity) > math.MaxUint32 {
		return fmt.Errorf("etf: tuple arity %d exceeds uint32", arity)
	}
	if arity <= 255 {
		w.buf = append(w.buf, TagSmallTuple, byte(arity))
		return nil
	}
	w.buf = append(w.buf, TagLargeTuple)
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(arity))
	return nil
}

func (w *wireWriter) writeInt64(v int64) {
	if v >= 0 && v <= 255 {
		w.buf = append(w.buf, TagSmallInt, byte(v))
		return
	}
	if v >= math.MinInt32 && v <= math.MaxInt32 {
		w.buf = append(w.buf, TagInt)
		w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(int32(v)))
		return
	}
	var sign byte
	var mag uint64
	if v < 0 {
		sign = 1
		// Handles math.MinInt64 without signed overflow.
		mag = uint64(-(v + 1)) + 1
	} else {
		mag = uint64(v)
	}
	w.writeSmallBigUint64(sign, mag)
}

func (w *wireWriter) writeUint64(v uint64) {
	if v <= 255 {
		w.buf = append(w.buf, TagSmallInt, byte(v))
		return
	}
	if v <= math.MaxInt32 {
		w.buf = append(w.buf, TagInt)
		w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
		return
	}
	w.writeSmallBigUint64(0, v)
}

func (w *wireWriter) writeSmallBigUint64(sign byte, mag uint64) {
	var digits [8]byte
	n := 0
	for mag > 0 {
		digits[n] = byte(mag)
		mag >>= 8
		n++
	}
	w.buf = append(w.buf, TagSmallBig, byte(n), sign)
	w.buf = append(w.buf, digits[:n]...)
}

func (w *wireWriter) writeBigInt(v *big.Int) error {
	if v == nil {
		w.writeInt64(0)
		return nil
	}
	if v.IsInt64() {
		w.writeInt64(v.Int64())
		return nil
	}
	if v.IsUint64() {
		w.writeUint64(v.Uint64())
		return nil
	}

	be := v.Bytes() // big-endian unsigned magnitude
	n := len(be)
	if uint64(n) > math.MaxUint32 {
		return fmt.Errorf("etf: bignum byte length %d exceeds uint32", n)
	}
	var sign byte
	if v.Sign() < 0 {
		sign = 1
	}

	if n <= 255 {
		w.buf = append(w.buf, TagSmallBig, byte(n), sign)
	} else {
		w.buf = append(w.buf, TagLargeBig)
		w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(n))
		w.buf = append(w.buf, sign)
	}
	// ETF bignum digits are little-endian (LSB first).
	for i := n - 1; i >= 0; i-- {
		w.buf = append(w.buf, be[i])
	}
	return nil
}

func (w *wireWriter) writeFloat64(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ErrNonFiniteFloat
	}
	w.buf = append(w.buf, TagNewFloat)
	w.buf = binary.BigEndian.AppendUint64(w.buf, math.Float64bits(v))
	return nil
}

const (
	maxAtomRunes     = 255
	maxAtomUTF8Bytes = maxAtomRunes * utf8.UTFMax // 1020
)

func validateAtom(s string) error {
	if !utf8.ValidString(s) {
		return ErrInvalidUTF8
	}
	if len(s) > maxAtomUTF8Bytes || utf8.RuneCountInString(s) > maxAtomRunes {
		return ErrAtomTooLong
	}
	return nil
}

func (w *wireWriter) writeAtom(s string) error {
	if err := validateAtom(s); err != nil {
		return err
	}
	if len(s) <= 255 {
		w.buf = append(w.buf, TagSmallAtomUTF8, byte(len(s)))
		w.buf = append(w.buf, s...)
		return nil
	}
	w.buf = append(w.buf, TagAtomUTF8)
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(len(s)))
	w.buf = append(w.buf, s...)
	return nil
}

func (w *wireWriter) writeBool(v bool) {
	if v {
		w.buf = append(w.buf, TagSmallAtomUTF8, 4, 't', 'r', 'u', 'e')
	} else {
		w.buf = append(w.buf, TagSmallAtomUTF8, 5, 'f', 'a', 'l', 's', 'e')
	}
}

func (w *wireWriter) writeBinary(b []byte) error {
	if uint64(len(b)) > math.MaxUint32 {
		return fmt.Errorf("etf: binary length %d exceeds uint32", len(b))
	}
	w.buf = append(w.buf, TagBinary)
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(len(b)))
	w.buf = append(w.buf, b...)
	return nil
}

func (w *wireWriter) writeBinaryString(s string) error {
	if uint64(len(s)) > math.MaxUint32 {
		return fmt.Errorf("etf: binary length %d exceeds uint32", len(s))
	}
	w.buf = append(w.buf, TagBinary)
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(len(s)))
	w.buf = append(w.buf, s...)
	return nil
}

func (w *wireWriter) writeBitstring(bs Bitstring) error {
	if len(bs.Data) == 0 {
		if bs.Bits != 0 {
			return ErrInvalidBitstring
		}
		return w.writeBinary(nil)
	}
	if bs.Bits < 1 || bs.Bits > 8 {
		return ErrInvalidBitstring
	}
	if uint64(len(bs.Data)) > math.MaxUint32 {
		return fmt.Errorf("etf: bitstring length %d exceeds uint32", len(bs.Data))
	}
	if bs.Bits == 8 {
		return w.writeBinary(bs.Data)
	}
	w.buf = append(w.buf, TagBitBinary)
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(len(bs.Data)))
	w.buf = append(w.buf, bs.Bits)
	w.buf = append(w.buf, bs.Data...)
	// Zero unused low bits in final byte for canonical ETF representation.
	mask := byte(0xFF) << (8 - bs.Bits)
	w.buf[len(w.buf)-1] &= mask
	return nil
}

func (w *wireWriter) writeCharlist(s string) error {
	if len(s) == 0 {
		w.buf = append(w.buf, TagNil)
		return nil
	}
	if !utf8.ValidString(s) {
		return ErrInvalidUTF8
	}

	allLatin1 := true
	runeCount := 0
	for _, r := range s {
		if r > 255 {
			allLatin1 = false
		}
		runeCount++
	}

	if allLatin1 && runeCount <= 65535 {
		w.buf = append(w.buf, TagString)
		w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(runeCount))
		for _, r := range s {
			w.buf = append(w.buf, byte(r))
		}
		return nil
	}

	if uint64(runeCount) > math.MaxUint32 {
		return fmt.Errorf("etf: charlist length %d exceeds uint32", runeCount)
	}
	w.buf = append(w.buf, TagList)
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(runeCount))
	for _, r := range s {
		w.writeInt64(int64(r))
	}
	w.buf = append(w.buf, TagNil)
	return nil
}

func (w *wireWriter) writeParsedMapKey(pmk parsedMapKey) error {
	switch pmk.keyType {
	case KeyAtom:
		return w.writeAtom(pmk.rawName)
	case KeyBinary:
		return w.writeBinaryString(pmk.rawName)
	case KeyCharlist:
		return w.writeCharlist(pmk.rawName)
	case KeyInteger:
		return w.writeBigInt(pmk.intVal)
	case KeyFloat:
		return w.writeFloat64(pmk.floatVal)
	case KeyBoolean:
		w.writeBool(pmk.boolVal)
		return nil
	default:
		return fmt.Errorf("etf: unsupported map key type %v", pmk.keyType)
	}
}

func wrapETFOutput(payload []byte, compressionLevel int) ([]byte, error) {
	if compressionLevel == noCompression {
		out := make([]byte, 1+len(payload))
		out[0] = VersionMagic
		copy(out[1:], payload)
		return out, nil
	}
	if uint64(len(payload)) > math.MaxUint32 {
		return nil, fmt.Errorf("etf: compressed payload uncompressed size %d exceeds uint32", len(payload))
	}
	var zbuf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&zbuf, compressionLevel)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(payload); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	comp := zbuf.Bytes()
	out := make([]byte, 6+len(comp))
	out[0] = VersionMagic
	out[1] = TagCompressed
	binary.BigEndian.PutUint32(out[2:6], uint32(len(payload)))
	copy(out[6:], comp)
	return out, nil
}
