package etf

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

var unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()

func buildDecodeConfig(opts []DecodeOption) decodeConfig {
	cfg := defaultDecodeConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func validateDecodeTarget(v any, cfg decodeConfig) (reflect.Value, error) {
	if v == nil {
		return reflect.Value{}, ErrInvalidTarget
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return reflect.Value{}, ErrInvalidTarget
	}
	if cfg.nilAtomErr != nil {
		return reflect.Value{}, cfg.nilAtomErr
	}
	return rv, nil
}

func decodePayload(payload []byte, rv reflect.Value, cfg decodeConfig) error {
	d := decodeState{
		cfg: cfg,
		r:   newWireReader(payload),
	}
	d.path = d.pathBuf[:0]
	if err := d.decodeValue(rv, nil, cfg.nilAtom, 0); err != nil {
		return err
	}
	if d.r.remaining() != 0 {
		return ErrTrailingBytes
	}
	return nil
}

// Unmarshal parses an ETF-encoded payload (starting with VersionMagic 131)
// and stores the result in the value pointed to by v.
func Unmarshal(data []byte, v any, opts ...DecodeOption) error {
	cfg := buildDecodeConfig(opts)
	rv, err := validateDecodeTarget(v, cfg)
	if err != nil {
		return err
	}
	payload, err := unwrapETFPayload(data, cfg.maxUncompressedSize)
	if err != nil {
		return err
	}
	return decodePayload(payload, rv, cfg)
}

// Decoder reads and decodes ETF values from an input stream.
type Decoder struct {
	br  *bufio.Reader
	cfg decodeConfig
}

// NewDecoder returns a new Decoder that reads from r.
func NewDecoder(r io.Reader, opts ...DecodeOption) *Decoder {
	cfg := buildDecodeConfig(opts)
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &Decoder{br: br, cfg: cfg}
}

// Decode reads the next ETF-encoded term from its input and stores it in v.
func (dec *Decoder) Decode(v any) error {
	rv, err := validateDecodeTarget(v, dec.cfg)
	if err != nil {
		return err
	}
	payload, err := readStandaloneTermFromReader(dec.br, dec.cfg)
	if err != nil {
		return err
	}
	return decodePayload(payload, rv, dec.cfg)
}

type pathFrameKind uint8

const (
	pathField pathFrameKind = iota
	pathIndex
	pathMapKeyIdx
	pathMapVal
	pathTail
)

type pathFrame struct {
	kind   pathFrameKind
	name   string
	idx    int
	key    any
	keyVal reflect.Value
}

type decodeState struct {
	cfg     decodeConfig
	r       *wireReader
	path    []pathFrame
	pathBuf [16]pathFrame
}

func (d *decodeState) pushPath(f pathFrame) {
	d.path = append(d.path, f)
}

func (d *decodeState) popPath() {
	d.path = d.path[:len(d.path)-1]
}

func (d *decodeState) formatPath() string {
	if len(d.path) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, f := range d.path {
		switch f.kind {
		case pathField:
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.WriteString(f.name)
		case pathIndex:
			sb.WriteByte('[')
			sb.WriteString(strconv.Itoa(f.idx))
			sb.WriteByte(']')
		case pathMapKeyIdx:
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.WriteString("key#")
			sb.WriteString(strconv.Itoa(f.idx))
		case pathMapVal:
			sb.WriteByte('[')
			if f.keyVal.IsValid() {
				fmt.Fprint(&sb, f.keyVal.Interface())
			} else {
				fmt.Fprint(&sb, f.key)
			}
			sb.WriteByte(']')
		case pathTail:
			sb.WriteString(".tail")
		}
	}
	return sb.String()
}

func (d *decodeState) wrapErr(tag byte, typ reflect.Type, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*DecodeError); ok {
		return err
	}
	if _, ok := err.(*InvalidTagError); ok {
		return err
	}
	return &DecodeError{
		Path: d.formatPath(),
		Tag:  tag,
		Type: typ,
		Err:  err,
	}
}

func (d *decodeState) decodeValue(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	if depth > d.cfg.maxDepth {
		return d.wrapErr(0, v.Type(), ErrMaxDepthExceeded)
	}

	// Handle pointer targets and nilAtom (e.g. Erlang `undefined` -> Go nil pointer).
	for v.Kind() == reflect.Pointer {
		// If the incoming term is an atom equal to nilAtom and this pointer is assignable
		// (e.g. struct field *T, slice element *T, or **T), set pointer to nil.
		if v.CanSet() {
			if ok, err := d.tryConsumeNilAtom(nilAtom); err != nil {
				return d.wrapErr(0, v.Type(), err)
			} else if ok {
				v.Set(reflect.Zero(v.Type()))
				return nil
			}
		}

		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}

		// Check if pointer implements Unmarshaler (e.g. *CustomType).
		if v.Type() != bigIntPtrType && v.Type() != tuplePtrType && v.Type().Implements(unmarshalerType) {
			return d.decodeUnmarshaler(v.Interface().(Unmarshaler), spec, depth)
		}

		// Handle *big.Int directly.
		if v.Type() == bigIntPtrType {
			return d.decodeBigIntTarget(v.Elem(), spec)
		}

		v = v.Elem()
	}

	// Check addressable Unmarshaler (e.g. non-pointer RawTerm or CustomType field inside a struct).
	if v.CanAddr() && v.Type() != bigIntType && v.Type() != tupleType && reflect.PointerTo(v.Type()).Implements(unmarshalerType) {
		return d.decodeUnmarshaler(v.Addr().Interface().(Unmarshaler), spec, depth)
	}

	if v.Type() == bigIntType {
		return d.decodeBigIntTarget(v, spec)
	}

	// Interface target (any / interface{}).
	if v.Kind() == reflect.Interface {
		if v.NumMethod() != 0 {
			return d.wrapErr(0, v.Type(), fmt.Errorf("%w: cannot decode into non-empty interface %s", ErrTypeMismatch, v.Type()))
		}
		val, err := d.decodeAny(spec, nilAtom, depth)
		if err != nil {
			return err
		}
		if val == nil {
			v.Set(reflect.Zero(v.Type()))
		} else {
			v.Set(reflect.ValueOf(val))
		}
		return nil
	}

	tag, err := d.r.peekByte()
	if err != nil {
		return d.wrapErr(0, v.Type(), err)
	}

	// Enforce explicit TypeSpec if provided.
	if spec != nil && spec.kind != valAny {
		if err := validateTagForSpec(tag, spec); err != nil {
			return d.wrapErr(tag, v.Type(), err)
		}
	}

	switch tagTable[tag] {
	case tagInteger:
		return d.decodeIntegerInto(v, spec)
	case tagFloat:
		return d.decodeFloatInto(v, spec)
	case tagAtom:
		return d.decodeAtomInto(v, spec)
	case tagBinary:
		return d.decodeBinaryInto(v, spec)
	case tagBitstring:
		return d.decodeBitstringInto(v, spec)
	case tagNil:
		return d.decodeNilInto(v, spec)
	case tagString:
		return d.decodeStringExtInto(v, spec)
	case tagList:
		return d.decodeListInto(v, spec, nilAtom, depth)
	case tagTuple:
		return d.decodeTupleInto(v, spec, nilAtom, depth)
	case tagMap:
		return d.decodeMapInto(v, spec, nilAtom, depth)
	default:
		return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: %d", ErrUnsupportedTag, tag))
	}
}

func (d *decodeState) tryConsumeNilAtom(nilAtom Atom) (bool, error) {
	tag, err := d.r.peekByte()
	if err != nil {
		return false, err
	}
	if tagTable[tag] != tagAtom {
		return false, nil
	}
	saved := d.r.pos
	_, _ = d.r.readByte()
	s, err := d.r.readAtom(tag)
	if err != nil {
		d.r.pos = saved
		return false, err
	}
	if Atom(s) == nilAtom {
		return true, nil
	}
	d.r.pos = saved
	return false, nil
}

func (d *decodeState) decodeUnmarshaler(u Unmarshaler, spec *typeSpec, depth int) error {
	start := d.r.pos
	tag, err := d.r.peekByte()
	if err != nil {
		return d.wrapErr(0, reflect.TypeOf(u), err)
	}
	if err := d.r.skipAndValidateTerm(spec, depth, d.cfg.maxDepth); err != nil {
		return d.wrapErr(tag, reflect.TypeOf(u), err)
	}
	end := d.r.pos
	standalone := make([]byte, 1+(end-start))
	standalone[0] = VersionMagic
	copy(standalone[1:], d.r.data[start:end])
	if err := u.UnmarshalETF(standalone); err != nil {
		return d.wrapErr(d.r.data[start], reflect.TypeOf(u), err)
	}
	return nil
}

func validateTagForSpec(tag byte, spec *typeSpec) error {
	tk := tagTable[tag]
	switch spec.kind {
	case valInteger:
		if tk != tagInteger {
			return fmt.Errorf("%w: expected integer, got tag %d", ErrTypeMismatch, tag)
		}
	case valFloat:
		if tk != tagFloat {
			return fmt.Errorf("%w: expected float, got tag %d", ErrTypeMismatch, tag)
		}
	case valNumber:
		if tk != tagInteger && tk != tagFloat {
			return fmt.Errorf("%w: expected number, got tag %d", ErrTypeMismatch, tag)
		}
	case valAtom, valBoolean:
		if tk != tagAtom {
			return fmt.Errorf("%w: expected %s, got tag %d", ErrTypeMismatch, spec.rawText, tag)
		}
	case valBinary:
		if tk != tagBinary && tk != tagBitstring {
			return fmt.Errorf("%w: expected binary, got tag %d", ErrTypeMismatch, tag)
		}
	case valBitstring:
		if tk != tagBitstring && tk != tagBinary {
			return fmt.Errorf("%w: expected bitstring, got tag %d", ErrTypeMismatch, tag)
		}
	case valCharlist, valList:
		if tk != tagNil && tk != tagString && tk != tagList {
			return fmt.Errorf("%w: expected %s, got tag %d", ErrTypeMismatch, spec.rawText, tag)
		}
	case valMap:
		if tk != tagMap {
			return fmt.Errorf("%w: expected map, got tag %d", ErrTypeMismatch, tag)
		}
	case valTuple, valDateTime:
		if tk != tagTuple {
			return fmt.Errorf("%w: expected %s, got tag %d", ErrTypeMismatch, spec.rawText, tag)
		}
	}
	return nil
}

func (d *decodeState) decodeBigIntTarget(v reflect.Value, spec *typeSpec) error {
	tag, err := d.r.peekByte()
	if err != nil {
		return d.wrapErr(0, v.Type(), err)
	}
	if spec != nil && spec.kind != valAny {
		if err := validateTagForSpec(tag, spec); err != nil {
			return d.wrapErr(tag, v.Type(), err)
		}
	}
	switch tagTable[tag] {
	case tagInteger:
		i64, bi, t, err := d.r.readAnyInteger()
		if err != nil {
			return d.wrapErr(t, v.Type(), err)
		}
		if bi == nil {
			bi = big.NewInt(i64)
		}
		v.Set(reflect.ValueOf(*bi))
		return nil
	case tagFloat:
		if d.cfg.strictTypes && (spec == nil || spec.kind == valAny) {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: float cannot decode into *big.Int in strict mode", ErrTypeMismatch))
		}
		_, _ = d.r.readByte()
		f, err := d.r.readFloat()
		if err != nil {
			return d.wrapErr(tag, v.Type(), err)
		}
		if math.Trunc(f) != f {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: float %g is not an exact integer", ErrLossyCoercion, f))
		}
		bi, _ := new(big.Float).SetFloat64(f).Int(nil)
		v.Set(reflect.ValueOf(*bi))
		return nil
	default:
		return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: cannot decode tag %d into *big.Int", ErrTypeMismatch, tag))
	}
}

func (d *decodeState) decodeIntegerInto(v reflect.Value, spec *typeSpec) error {
	i64, bi, tag, err := d.r.readAnyInteger()
	if err != nil {
		return d.wrapErr(tag, v.Type(), err)
	}
	if err := assignIntegerToValue(v, i64, bi, d.cfg.strictTypes && (spec == nil || spec.kind == valAny)); err != nil {
		return d.wrapErr(tag, v.Type(), err)
	}
	return nil
}

func assignIntegerToValue(v reflect.Value, i64 int64, bi *big.Int, strict bool) error {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if bi != nil {
			return fmt.Errorf("%w: integer %s overflows %s", ErrOverflow, bi.String(), v.Type())
		}
		if v.OverflowInt(i64) {
			return fmt.Errorf("%w: integer %d overflows %s", ErrOverflow, i64, v.Type())
		}
		v.SetInt(i64)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		var u64 uint64
		if bi != nil {
			if bi.Sign() < 0 || !bi.IsUint64() {
				return fmt.Errorf("%w: integer %s overflows %s", ErrOverflow, bi.String(), v.Type())
			}
			u64 = bi.Uint64()
		} else {
			if i64 < 0 {
				return fmt.Errorf("%w: negative integer %d overflows %s", ErrOverflow, i64, v.Type())
			}
			u64 = uint64(i64)
		}
		if v.OverflowUint(u64) {
			return fmt.Errorf("%w: integer %d overflows %s", ErrOverflow, u64, v.Type())
		}
		v.SetUint(u64)
		return nil

	case reflect.Float32, reflect.Float64:
		if strict {
			return fmt.Errorf("%w: integer cannot decode into %s in strict mode", ErrTypeMismatch, v.Type())
		}
		var f float64
		if bi != nil {
			f, _ = new(big.Float).SetInt(bi).Float64()
		} else {
			f = float64(i64)
		}
		if math.IsInf(f, 0) || v.OverflowFloat(f) {
			return fmt.Errorf("%w: integer overflows %s", ErrOverflow, v.Type())
		}
		v.SetFloat(f)
		return nil

	default:
		return fmt.Errorf("%w: cannot decode integer into Go %s", ErrTypeMismatch, v.Type())
	}
}

func (d *decodeState) decodeFloatInto(v reflect.Value, spec *typeSpec) error {
	tag, err := d.r.readByte()
	if err != nil {
		return d.wrapErr(0, v.Type(), err)
	}
	f, err := d.r.readFloat()
	if err != nil {
		return d.wrapErr(tag, v.Type(), err)
	}

	strict := d.cfg.strictTypes && (spec == nil || spec.kind == valAny)
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		if v.OverflowFloat(f) {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: float %g overflows %s", ErrOverflow, f, v.Type()))
		}
		v.SetFloat(f)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if strict {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: float cannot decode into %s in strict mode", ErrTypeMismatch, v.Type()))
		}
		if math.Trunc(f) != f {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: float %g is not an exact integer", ErrLossyCoercion, f))
		}
		bi, _ := new(big.Float).SetFloat64(f).Int(nil)
		var i64 int64
		var bigArg *big.Int
		if bi.IsInt64() {
			i64 = bi.Int64()
		} else {
			bigArg = bi
		}
		if err := assignIntegerToValue(v, i64, bigArg, false); err != nil {
			return d.wrapErr(tag, v.Type(), err)
		}
		return nil

	default:
		return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: cannot decode float into Go %s", ErrTypeMismatch, v.Type()))
	}
}

func (d *decodeState) decodeAtomInto(v reflect.Value, spec *typeSpec) error {
	tag, err := d.r.readByte()
	if err != nil {
		return d.wrapErr(0, v.Type(), err)
	}
	s, err := d.r.readAtom(tag)
	if err != nil {
		return d.wrapErr(tag, v.Type(), err)
	}

	if spec != nil && spec.kind == valBoolean {
		if s != "true" && s != "false" {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: expected boolean atom (true/false), got atom %q", ErrTypeMismatch, s))
		}
	}

	strict := d.cfg.strictTypes && (spec == nil || spec.kind == valAny)

	if v.Type() == atomType {
		v.SetString(s)
		return nil
	}

	switch v.Kind() {
	case reflect.Bool:
		if s == "true" {
			v.SetBool(true)
			return nil
		}
		if s == "false" {
			v.SetBool(false)
			return nil
		}
		return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: atom %q is not a boolean", ErrTypeMismatch, s))

	case reflect.String:
		if v.Type() == binaryType || v.Type() == charlistType {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: atom cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		if strict {
			return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: atom cannot decode into %s in strict mode", ErrTypeMismatch, v.Type()))
		}
		v.SetString(s)
		return nil

	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if strict {
				return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: atom cannot decode into []byte in strict mode", ErrTypeMismatch))
			}
			v.SetBytes([]byte(s))
			return nil
		}
	}
	return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: cannot decode atom %q into Go %s", ErrTypeMismatch, s, v.Type()))
}

func (d *decodeState) decodeBinaryInto(v reflect.Value, spec *typeSpec) error {
	_, _ = d.r.readByte()
	if v.Kind() == reflect.String {
		s, err := d.r.readBinaryString()
		if err != nil {
			return d.wrapErr(TagBinary, v.Type(), err)
		}
		if v.Type() == atomType || v.Type() == charlistType {
			return d.wrapErr(TagBinary, v.Type(), fmt.Errorf("%w: binary cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		v.SetString(s)
		return nil
	}

	b, err := d.r.readBinary()
	if err != nil {
		return d.wrapErr(TagBinary, v.Type(), err)
	}

	if v.Type() == bitstringType {
		var bits uint8
		if len(b) > 0 {
			bits = 8
		}
		v.Set(reflect.ValueOf(Bitstring{Data: b, Bits: bits}))
		return nil
	}
	if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
		v.SetBytes(b)
		return nil
	}
	return d.wrapErr(TagBinary, v.Type(), fmt.Errorf("%w: cannot decode binary into Go %s", ErrTypeMismatch, v.Type()))
}

func (d *decodeState) decodeBitstringInto(v reflect.Value, spec *typeSpec) error {
	_, _ = d.r.readByte()
	bs, err := d.r.readBitstring()
	if err != nil {
		return d.wrapErr(TagBitBinary, v.Type(), err)
	}

	if spec != nil && spec.kind == valBinary && len(bs.Data) > 0 && bs.Bits != 8 {
		return d.wrapErr(TagBitBinary, v.Type(), fmt.Errorf("%w: non-byte-aligned bitstring (bits=%d) does not match binary", ErrTypeMismatch, bs.Bits))
	}
	if v.Type() == bitstringType {
		v.Set(reflect.ValueOf(bs))
		return nil
	}
	if len(bs.Data) > 0 && bs.Bits != 8 {
		return d.wrapErr(TagBitBinary, v.Type(), fmt.Errorf("%w: non-byte-aligned bitstring (bits=%d) requires etf.Bitstring", ErrTypeMismatch, bs.Bits))
	}
	switch v.Kind() {
	case reflect.String:
		if v.Type() == atomType || v.Type() == charlistType {
			return d.wrapErr(TagBitBinary, v.Type(), fmt.Errorf("%w: bitstring cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		v.SetString(string(bs.Data))
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes(bs.Data)
			return nil
		}
	}
	return d.wrapErr(TagBitBinary, v.Type(), fmt.Errorf("%w: cannot decode bitstring into Go %s", ErrTypeMismatch, v.Type()))
}

func (d *decodeState) decodeNilInto(v reflect.Value, spec *typeSpec) error {
	_, _ = d.r.readByte()
	strict := d.cfg.strictTypes && (spec == nil || spec.kind == valAny)

	if v.Type() == tupleType {
		return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: cannot decode empty list into Go %s", ErrTypeMismatch, v.Type()))
	}
	if v.Type() == improperListType {
		v.Set(reflect.ValueOf(ImproperList{Elements: []any{}, Tail: []any{}}))
		return nil
	}
	switch v.Kind() {
	case reflect.Slice:
		if strict && v.Type().Elem().Kind() == reflect.Uint8 {
			return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: empty list cannot decode into []byte in strict mode", ErrTypeMismatch))
		}
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
		return nil
	case reflect.Array:
		if v.Len() != 0 {
			return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: empty list cannot decode into array of length %d", ErrTypeMismatch, v.Len()))
		}
		return nil
	case reflect.String:
		if v.Type() == atomType || v.Type() == binaryType {
			return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: empty list cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		if strict && v.Type() != charlistType {
			return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: empty list cannot decode into %s in strict mode", ErrTypeMismatch, v.Type()))
		}
		v.SetString("")
		return nil
	default:
		return d.wrapErr(TagNil, v.Type(), fmt.Errorf("%w: cannot decode empty list into Go %s", ErrTypeMismatch, v.Type()))
	}
}

func (d *decodeState) decodeStringExtInto(v reflect.Value, spec *typeSpec) error {
	_, _ = d.r.readByte()
	raw, err := d.r.readStringExt()
	if err != nil {
		return d.wrapErr(TagString, v.Type(), err)
	}

	strict := d.cfg.strictTypes && (spec == nil || spec.kind == valAny)
	var elemSpec *typeSpec
	if spec != nil && spec.kind == valList {
		elemSpec = spec.elem
	}

	// If list(T) was declared, verify T permits integer elements (bytes 0..255).
	if elemSpec != nil && elemSpec.kind != valAny && elemSpec.kind != valInteger && elemSpec.kind != valNumber {
		return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: list of bytes (STRING_EXT) does not match %s", ErrTypeMismatch, spec.rawText))
	}

	if v.Type() == tupleType {
		return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: cannot decode STRING_EXT into Go %s", ErrTypeMismatch, v.Type()))
	}

	switch v.Kind() {
	case reflect.String:
		if v.Type() == atomType || v.Type() == binaryType {
			return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: charlist (STRING_EXT) cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		if strict && v.Type() != charlistType {
			return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: charlist (STRING_EXT) cannot decode into %s in strict mode", ErrTypeMismatch, v.Type()))
		}
		v.SetString(latin1ToUTF8(raw))
		return nil

	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 && elemSpec == nil &&
			!reflect.PointerTo(v.Type().Elem()).Implements(unmarshalerType) {
			if strict {
				return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: STRING_EXT cannot decode into []byte in strict mode", ErrTypeMismatch))
			}
			cp := make([]byte, len(raw))
			copy(cp, raw)
			v.SetBytes(cp)
			return nil
		}
		slice := reflect.MakeSlice(v.Type(), 0, cappedSliceCap(len(raw), v.Type().Elem().Size()))
		for i, b := range raw {
			slice = growSlice(slice, i+1, len(raw))
			d.pushPath(pathFrame{kind: pathIndex, idx: i})
			if err := d.decodeSmallIntElement(slice.Index(i), b, strict && elemSpec == nil); err != nil {
				return err
			}
			d.popPath()
		}
		v.Set(slice)
		return nil

	case reflect.Array:
		if v.Len() != len(raw) {
			return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: list length %d does not match array length %d", ErrTypeMismatch, len(raw), v.Len()))
		}
		for i, b := range raw {
			d.pushPath(pathFrame{kind: pathIndex, idx: i})
			if err := d.decodeSmallIntElement(v.Index(i), b, strict && elemSpec == nil); err != nil {
				return err
			}
			d.popPath()
		}
		return nil

	default:
		return d.wrapErr(TagString, v.Type(), fmt.Errorf("%w: cannot decode STRING_EXT into Go %s", ErrTypeMismatch, v.Type()))
	}
}

func (d *decodeState) decodeSmallIntElement(v reflect.Value, b byte, strict bool) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		if v.Type() != bigIntPtrType && v.Type().Implements(unmarshalerType) {
			standalone := []byte{VersionMagic, TagSmallInt, b}
			if err := v.Interface().(Unmarshaler).UnmarshalETF(standalone); err != nil {
				return d.wrapErr(TagString, v.Type(), err)
			}
			return nil
		}
		if v.Type() == bigIntPtrType {
			v.Elem().Set(reflect.ValueOf(*big.NewInt(int64(b))))
			return nil
		}
		v = v.Elem()
	}
	if v.CanAddr() && v.Type() != bigIntType && reflect.PointerTo(v.Type()).Implements(unmarshalerType) {
		standalone := []byte{VersionMagic, TagSmallInt, b}
		if err := v.Addr().Interface().(Unmarshaler).UnmarshalETF(standalone); err != nil {
			return d.wrapErr(TagString, v.Type(), err)
		}
		return nil
	}
	if v.Type() == bigIntType {
		v.Set(reflect.ValueOf(*big.NewInt(int64(b))))
		return nil
	}
	if v.Kind() == reflect.Interface && v.NumMethod() == 0 {
		v.Set(reflect.ValueOf(int64(b)))
		return nil
	}
	if err := assignIntegerToValue(v, int64(b), nil, strict); err != nil {
		return d.wrapErr(TagString, v.Type(), err)
	}
	return nil
}

func (d *decodeState) decodeListInto(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	_, _ = d.r.readByte()
	n, err := d.r.readListHeader()
	if err != nil {
		return d.wrapErr(TagList, v.Type(), err)
	}

	var elemSpec *typeSpec
	if spec != nil && spec.kind == valList {
		elemSpec = spec.elem
	}

	// Decoding into ImproperList explicitly.
	if v.Type() == tupleType {
		return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: cannot decode list into Go %s", ErrTypeMismatch, v.Type()))
	}
	if v.Type() == improperListType {
		elems := make([]any, 0, cappedCap(int(n)))
		for i := uint32(0); i < n; i++ {
			d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
			val, err := d.decodeAny(elemSpec, nilAtom, depth+1)
			if err != nil {
				return err
			}
			d.popPath()
			elems = append(elems, val)
		}
		d.pushPath(pathFrame{kind: pathTail})
		tail, err := d.decodeAny(nil, nilAtom, depth+1)
		if err != nil {
			return err
		}
		d.popPath()
		v.Set(reflect.ValueOf(ImproperList{Elements: elems, Tail: tail}))
		return nil
	}

	// Decoding LIST_EXT of Unicode code points into string / Charlist.
	if v.Kind() == reflect.String {
		if elemSpec != nil && elemSpec.kind != valAny && elemSpec.kind != valInteger && elemSpec.kind != valNumber {
			return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: charlist (LIST_EXT) does not match %s", ErrTypeMismatch, spec.rawText))
		}
		if v.Type() == atomType || v.Type() == binaryType {
			return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: list cannot decode into %s", ErrTypeMismatch, v.Type()))
		}
		strict := d.cfg.strictTypes && (spec == nil || spec.kind == valAny)
		if strict && v.Type() != charlistType {
			return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: list cannot decode into %s in strict mode", ErrTypeMismatch, v.Type()))
		}
		s, err := d.readCharlistBody(n, depth)
		if err != nil {
			return d.wrapErr(TagList, v.Type(), err)
		}
		v.SetString(s)
		return nil
	}

	switch v.Kind() {
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 0, cappedSliceCap(int(n), v.Type().Elem().Size()))
		for i := uint32(0); i < n; i++ {
			slice = growSlice(slice, int(i)+1, int(n))
			d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
			if err := d.decodeValue(slice.Index(int(i)), elemSpec, nilAtom, depth+1); err != nil {
				return err
			}
			d.popPath()
		}
		tailTag, err := d.r.readByte()
		if err != nil {
			return d.wrapErr(TagList, v.Type(), err)
		}
		if tailTag != TagNil {
			return d.wrapErr(tailTag, v.Type(), ErrImproperList)
		}
		v.Set(slice)
		return nil

	case reflect.Array:
		if uint32(v.Len()) != n {
			return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: list length %d does not match array length %d", ErrTypeMismatch, n, v.Len()))
		}
		for i := uint32(0); i < n; i++ {
			d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
			if err := d.decodeValue(v.Index(int(i)), elemSpec, nilAtom, depth+1); err != nil {
				return err
			}
			d.popPath()
		}
		tailTag, err := d.r.readByte()
		if err != nil {
			return d.wrapErr(TagList, v.Type(), err)
		}
		if tailTag != TagNil {
			return d.wrapErr(tailTag, v.Type(), ErrImproperList)
		}
		return nil

	default:
		return d.wrapErr(TagList, v.Type(), fmt.Errorf("%w: cannot decode list into Go %s", ErrTypeMismatch, v.Type()))
	}
}

func (d *decodeState) decodeTupleInto(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	tag, err := d.r.readByte()
	if err != nil {
		return d.wrapErr(0, v.Type(), err)
	}
	if v.Type() != tupleType {
		return d.wrapErr(tag, v.Type(), fmt.Errorf("%w: cannot decode tuple into Go %s (type must implement etf.Unmarshaler)", ErrTypeMismatch, v.Type()))
	}
	arity, err := d.r.readTupleHeader(tag)
	if err != nil {
		return d.wrapErr(tag, v.Type(), err)
	}
	elems := make(Tuple, 0, cappedCap(int(arity)))
	for i := uint32(0); i < arity; i++ {
		d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
		val, err := d.decodeAny(nil, nilAtom, depth+1)
		if err != nil {
			return err
		}
		d.popPath()
		elems = append(elems, val)
	}
	if spec != nil && spec.kind == valDateTime {
		if _, err := dateTimeFromTuple(elems); err != nil {
			return d.wrapErr(tag, v.Type(), err)
		}
	}
	v.Set(reflect.ValueOf(elems))
	return nil
}

func (d *decodeState) readCharlistBody(n uint32, depth int) (string, error) {
	var sb strings.Builder
	if err := d.r.readCharlistBody(n, depth, d.cfg.maxDepth, &sb); err != nil {
		return "", err
	}
	return sb.String(), nil
}

const (
	maxPreallocCap   = 1024
	maxPreallocBytes = 64 << 10 // 64 KiB initial slice backing allocation cap
)

func cappedCap(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxPreallocCap {
		return maxPreallocCap
	}
	return n
}

func cappedSliceCap(n int, elemSize uintptr) int {
	c := cappedCap(n)
	if elemSize > 0 {
		if maxBySize := int(maxPreallocBytes / elemSize); maxBySize < c {
			c = maxBySize
		}
		if c == 0 && n > 0 {
			c = 1
		}
	}
	return c
}

func growSlice(slice reflect.Value, targetLen, maxLen int) reflect.Value {
	if targetLen <= slice.Cap() {
		return slice.Slice(0, targetLen)
	}
	newCap := slice.Cap() * 2
	if newCap < targetLen {
		newCap = targetLen
	}
	if newCap > maxLen {
		newCap = maxLen
	}
	grown := reflect.MakeSlice(slice.Type(), targetLen, newCap)
	reflect.Copy(grown, slice)
	return grown
}
