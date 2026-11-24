package etf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"unicode/utf8"
)

var (
	marshalerType    = reflect.TypeOf((*Marshaler)(nil)).Elem()
	bigIntType       = reflect.TypeOf(big.Int{})
	bigIntPtrType    = reflect.TypeOf((*big.Int)(nil))
	atomType         = reflect.TypeOf(Atom(""))
	binaryType       = reflect.TypeOf(Binary(""))
	charlistType     = reflect.TypeOf(Charlist(""))
	bitstringType    = reflect.TypeOf(Bitstring{})
	etfMapType       = reflect.TypeOf(Map(nil))
	improperListType = reflect.TypeOf(ImproperList{})
	tupleType        = reflect.TypeOf(Tuple(nil))
	tuplePtrType     = reflect.TypeOf((*Tuple)(nil))
)

func buildEncodeConfig(opts []EncodeOption) encodeConfig {
	cfg := defaultEncodeConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func encodeTerm(w *wireWriter, cfg encodeConfig, v any) ([]byte, error) {
	if cfg.nilAtomErr != nil {
		return nil, cfg.nilAtomErr
	}
	w.reset()
	if cfg.compressionLevel == noCompression {
		w.writeByte(VersionMagic)
	}
	e := encodeState{cfg: cfg, w: w}
	if err := e.encodeAny(v, nil, cfg.nilAtom, 0); err != nil {
		return nil, err
	}
	if cfg.compressionLevel == noCompression {
		return w.bytes(), nil
	}
	return wrapETFOutput(w.bytes(), cfg.compressionLevel)
}

// Marshal encodes v into the Erlang External Term Format (starting with VersionMagic 131).
func Marshal(v any, opts ...EncodeOption) ([]byte, error) {
	var w wireWriter
	return encodeTerm(&w, buildEncodeConfig(opts), v)
}

// Encoder writes ETF-encoded values to an output stream.
type Encoder struct {
	w   io.Writer
	cfg encodeConfig
	buf wireWriter
}

// NewEncoder returns a new Encoder that writes to w.
func NewEncoder(w io.Writer, opts ...EncodeOption) *Encoder {
	return &Encoder{w: w, cfg: buildEncodeConfig(opts)}
}

// Encode writes the ETF encoding of v to the stream.
func (enc *Encoder) Encode(v any) error {
	out, err := encodeTerm(&enc.buf, enc.cfg, v)
	if err == nil {
		_, err = enc.w.Write(out)
	}
	if cap(enc.buf.buf) > maxPreallocBytes {
		enc.buf.buf = nil
	}
	return err
}

type encodeState struct {
	cfg encodeConfig
	w   *wireWriter
}

func (e *encodeState) encodeAny(v any, spec *typeSpec, nilAtom Atom, depth int) error {
	if v == nil {
		return e.w.writeAtom(string(nilAtom))
	}
	return e.encodeValue(reflect.ValueOf(v), spec, nilAtom, depth)
}

func (e *encodeState) encodeValue(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	if depth > e.cfg.maxDepth {
		return ErrMaxDepthExceeded
	}
	if !v.IsValid() {
		return e.w.writeAtom(string(nilAtom))
	}

	// Unwrap interface values first.
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return e.w.writeAtom(string(nilAtom))
		}
		return e.encodeValue(v.Elem(), spec, nilAtom, depth)
	}

	// Check nil pointers before checking Marshaler (avoid nil pointer receiver panic).
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return e.w.writeAtom(string(nilAtom))
	}

	// Handle Tuple natively to preserve encodeConfig (nilAtom, maxDepth) and avoid double-encoding.
	if v.Type() == tuplePtrType {
		return e.encodeTupleValue(*v.Interface().(*Tuple), spec, nilAtom, depth)
	}
	if v.Type() == tupleType {
		return e.encodeTupleValue(v.Interface().(Tuple), spec, nilAtom, depth)
	}

	// Check Marshaler on value or addressable pointer.
	if m, ok := asMarshaler(v); ok {
		return e.encodeMarshaler(m, spec, depth)
	}

	// Unwrap non-nil pointers (except *big.Int handled below).
	if v.Kind() == reflect.Pointer {
		if v.Type() == bigIntPtrType {
			return e.encodeBigInt(v.Interface().(*big.Int), spec)
		}
		return e.encodeValue(v.Elem(), spec, nilAtom, depth)
	}

	if v.Type() == bigIntType {
		bi := v.Interface().(big.Int)
		return e.encodeBigInt(&bi, spec)
	}

	// If an explicit Erlang TypeSpec is declared, honor it strictly.
	if spec != nil && spec.kind != valAny {
		return e.encodeWithSpec(v, spec, nilAtom, depth)
	}

	// Default encoding based on Go type.
	return e.encodeDefault(v, nilAtom, depth)
}

func asMarshaler(v reflect.Value) (Marshaler, bool) {
	if v.Type().Implements(marshalerType) {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return nil, false
		}
		return v.Interface().(Marshaler), true
	}
	if v.CanAddr() && reflect.PointerTo(v.Type()).Implements(marshalerType) {
		return v.Addr().Interface().(Marshaler), true
	}
	return nil, false
}

func (e *encodeState) encodeMarshaler(m Marshaler, spec *typeSpec, depth int) error {
	raw, err := m.MarshalETF()
	if err != nil {
		return err
	}
	payload, err := unwrapETFPayload(raw, defaultMaxUncompressedSize)
	if err != nil {
		return fmt.Errorf("etf: Marshaler returned invalid ETF term: %w", err)
	}
	if len(payload) == 0 {
		return fmt.Errorf("etf: Marshaler returned invalid ETF term: %w", ErrUnexpectedEOF)
	}
	r := newWireReader(payload)
	if err := r.skipAndValidateTerm(spec, depth, e.cfg.maxDepth); err != nil {
		if errors.Is(err, ErrTypeMismatch) || errors.Is(err, ErrImproperList) || errors.Is(err, ErrInvalidDateTime) {
			return err
		}
		return fmt.Errorf("etf: Marshaler returned malformed ETF term: %w", err)
	}
	if r.remaining() != 0 {
		return fmt.Errorf("etf: Marshaler returned %w", ErrTrailingBytes)
	}
	e.w.buf = append(e.w.buf, payload...)
	return nil
}

func (e *encodeState) encodeBigInt(bi *big.Int, spec *typeSpec) error {
	if spec == nil || spec.kind == valAny || spec.kind == valInteger || spec.kind == valNumber {
		return e.w.writeBigInt(bi)
	}
	if spec.kind == valFloat {
		f, _ := new(big.Float).SetInt(bi).Float64()
		return e.w.writeFloat64(f)
	}
	return fmt.Errorf("%w: cannot encode *big.Int as %s", ErrTypeMismatch, spec.rawText)
}

func (e *encodeState) encodeWithSpec(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	switch spec.kind {
	case valInteger:
		return e.encodeAsInteger(v)
	case valFloat:
		return e.encodeAsFloat(v)
	case valNumber:
		switch v.Kind() {
		case reflect.Float32, reflect.Float64:
			return e.encodeFloatAsNumber(v.Float())
		default:
			return e.encodeAsInteger(v)
		}
	case valAtom:
		return e.encodeAsAtom(v)
	case valBoolean:
		return e.encodeAsBoolean(v)
	case valBinary:
		return e.encodeAsBinary(v)
	case valBitstring:
		return e.encodeAsBitstring(v)
	case valCharlist:
		return e.encodeAsCharlist(v)
	case valList:
		return e.encodeAsList(v, spec.elem, nilAtom, depth)
	case valMap:
		return e.encodeAsMap(v, nilAtom, depth)
	case valTuple:
		return fmt.Errorf("%w: cannot encode Go %s as ETF tuple (type must implement etf.Marshaler)", ErrTypeMismatch, v.Type())
	case valDateTime:
		return fmt.Errorf("%w: cannot encode Go %s as ETF datetime (type must implement etf.Marshaler, e.g. etf.DateTime)", ErrTypeMismatch, v.Type())
	default:
		return e.encodeDefault(v, nilAtom, depth)
	}
}

func (e *encodeState) encodeFloatAsNumber(f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ErrNonFiniteFloat
	}
	if math.Trunc(f) == f {
		if f >= math.MinInt64 && f < 9223372036854775808.0 {
			e.w.writeInt64(int64(f))
			return nil
		}
		bi, _ := new(big.Float).SetFloat64(f).Int(nil)
		return e.w.writeBigInt(bi)
	}
	return e.w.writeFloat64(f)
}

func (e *encodeState) encodeAsInteger(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.w.writeInt64(v.Int())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.w.writeUint64(v.Uint())
		return nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrNonFiniteFloat
		}
		if math.Trunc(f) != f {
			return fmt.Errorf("%w: float %g is not an exact integer", ErrLossyCoercion, f)
		}
		bi, _ := new(big.Float).SetFloat64(f).Int(nil)
		return e.w.writeBigInt(bi)
	default:
		return fmt.Errorf("%w: cannot encode Go %s as ETF integer", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeAsFloat(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return e.w.writeFloat64(v.Float())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return e.w.writeFloat64(float64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return e.w.writeFloat64(float64(v.Uint()))
	default:
		return fmt.Errorf("%w: cannot encode Go %s as ETF float", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeAsAtom(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		return e.w.writeAtom(v.String())
	case reflect.Bool:
		e.w.writeBool(v.Bool())
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return e.w.writeAtom(string(v.Bytes()))
		}
	}
	return fmt.Errorf("%w: cannot encode Go %s as ETF atom", ErrTypeMismatch, v.Type())
}

func (e *encodeState) encodeAsBoolean(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Bool:
		e.w.writeBool(v.Bool())
		return nil
	case reflect.String:
		s := v.String()
		if s == "true" {
			e.w.writeBool(true)
			return nil
		}
		if s == "false" {
			e.w.writeBool(false)
			return nil
		}
		return fmt.Errorf("%w: %q is not a valid ETF boolean (expected true or false)", ErrTypeMismatch, s)
	default:
		return fmt.Errorf("%w: cannot encode Go %s as ETF boolean", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeAsBinary(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		return e.w.writeBinaryString(v.String())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return e.w.writeBinary(v.Bytes())
		}
	}
	if v.Type() == bitstringType {
		bs := v.Interface().(Bitstring)
		if len(bs.Data) == 0 && bs.Bits == 0 {
			return e.w.writeBinary(nil)
		}
		if bs.Bits == 8 {
			return e.w.writeBinary(bs.Data)
		}
		return fmt.Errorf("%w: non-byte-aligned bitstring cannot encode as binary", ErrTypeMismatch)
	}
	return fmt.Errorf("%w: cannot encode Go %s as ETF binary", ErrTypeMismatch, v.Type())
}

func (e *encodeState) encodeAsBitstring(v reflect.Value) error {
	if v.Type() == bitstringType {
		return e.w.writeBitstring(v.Interface().(Bitstring))
	}
	switch v.Kind() {
	case reflect.String:
		return e.w.writeBinaryString(v.String())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return e.w.writeBinary(v.Bytes())
		}
	}
	return fmt.Errorf("%w: cannot encode Go %s as ETF bitstring", ErrTypeMismatch, v.Type())
}

func (e *encodeState) encodeAsCharlist(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		return e.w.writeCharlist(v.String())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return e.w.writeCharlist(string(v.Bytes()))
		}
		if v.Type().Elem().Kind() == reflect.Int32 {
			runes := make([]rune, v.Len())
			for i := 0; i < v.Len(); i++ {
				r := rune(v.Index(i).Int())
				if !utf8.ValidRune(r) {
					return fmt.Errorf("%w: invalid Unicode code point %d", ErrInvalidUTF8, r)
				}
				runes[i] = r
			}
			return e.w.writeCharlist(string(runes))
		}
	}
	return fmt.Errorf("%w: cannot encode Go %s as ETF charlist", ErrTypeMismatch, v.Type())
}

func (e *encodeState) encodeAsList(v reflect.Value, elemSpec *typeSpec, nilAtom Atom, depth int) error {
	if v.Type() == tupleType {
		return fmt.Errorf("%w: cannot encode Go %s as ETF list", ErrTypeMismatch, v.Type())
	}
	if v.Type() == improperListType {
		return e.encodeImproperList(v.Interface().(ImproperList), elemSpec, nilAtom, depth)
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		n := v.Len()
		if n == 0 {
			e.w.writeByte(TagNil)
			return nil
		}
		if uint64(n) > math.MaxUint32 {
			return fmt.Errorf("etf: list length %d exceeds uint32", n)
		}
		e.w.writeByte(TagList)
		e.w.writeUint32(uint32(n))
		for i := 0; i < n; i++ {
			if err := e.encodeValue(v.Index(i), elemSpec, nilAtom, depth+1); err != nil {
				return err
			}
		}
		e.w.writeByte(TagNil)
		return nil
	default:
		return fmt.Errorf("%w: cannot encode Go %s as ETF list", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeImproperList(il ImproperList, elemSpec *typeSpec, nilAtom Atom, depth int) error {
	if len(il.Elements) == 0 {
		return e.encodeAny(il.Tail, nil, nilAtom, depth+1)
	}
	if uint64(len(il.Elements)) > math.MaxUint32 {
		return fmt.Errorf("etf: list length %d exceeds uint32", len(il.Elements))
	}
	e.w.writeByte(TagList)
	e.w.writeUint32(uint32(len(il.Elements)))
	for _, elem := range il.Elements {
		if err := e.encodeAny(elem, elemSpec, nilAtom, depth+1); err != nil {
			return err
		}
	}
	return e.encodeAny(il.Tail, nil, nilAtom, depth+1)
}

func (e *encodeState) encodeAsMap(v reflect.Value, nilAtom Atom, depth int) error {
	if v.Type() == etfMapType {
		return e.encodeETFMap(v.Interface().(Map), nilAtom, depth)
	}
	switch v.Kind() {
	case reflect.Map:
		return e.encodeGoMap(v, nilAtom, depth)
	case reflect.Struct:
		return e.encodeStruct(v, depth)
	default:
		return fmt.Errorf("%w: cannot encode Go %s as ETF map", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeDefault(v reflect.Value, nilAtom Atom, depth int) error {
	switch v.Type() {
	case atomType:
		return e.w.writeAtom(string(v.Interface().(Atom)))
	case binaryType:
		return e.w.writeBinaryString(string(v.Interface().(Binary)))
	case charlistType:
		return e.w.writeCharlist(string(v.Interface().(Charlist)))
	case bitstringType:
		return e.w.writeBitstring(v.Interface().(Bitstring))
	case etfMapType:
		return e.encodeETFMap(v.Interface().(Map), nilAtom, depth)
	case improperListType:
		return e.encodeImproperList(v.Interface().(ImproperList), nil, nilAtom, depth)
	case tupleType:
		return e.encodeTuple(v.Interface().(Tuple), nilAtom, depth)
	}

	switch v.Kind() {
	case reflect.Bool:
		e.w.writeBool(v.Bool())
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.w.writeInt64(v.Int())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.w.writeUint64(v.Uint())
		return nil
	case reflect.Float32, reflect.Float64:
		return e.w.writeFloat64(v.Float())
	case reflect.String:
		return e.w.writeBinaryString(v.String())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return e.w.writeBinary(v.Bytes())
		}
		return e.encodeAsList(v, nil, nilAtom, depth)
	case reflect.Array:
		return e.encodeAsList(v, nil, nilAtom, depth)
	case reflect.Map:
		return e.encodeGoMap(v, nilAtom, depth)
	case reflect.Struct:
		return e.encodeStruct(v, depth)
	default:
		return fmt.Errorf("%w: unsupported Go type %s", ErrTypeMismatch, v.Type())
	}
}

func (e *encodeState) encodeTupleValue(t Tuple, spec *typeSpec, nilAtom Atom, depth int) error {
	if spec != nil && spec.kind != valAny && spec.kind != valTuple {
		if spec.kind == valDateTime {
			if _, err := dateTimeFromTuple(t); err != nil {
				return err
			}
			return e.encodeTuple(t, nilAtom, depth)
		}
		return fmt.Errorf("%w: cannot encode etf.Tuple as %s", ErrTypeMismatch, spec.rawText)
	}
	return e.encodeTuple(t, nilAtom, depth)
}

func (e *encodeState) encodeTuple(t Tuple, nilAtom Atom, depth int) error {
	if err := e.w.writeTupleHeader(len(t)); err != nil {
		return err
	}
	for _, elem := range t {
		if err := e.encodeAny(elem, nil, nilAtom, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (e *encodeState) encodeETFMap(m Map, nilAtom Atom, depth int) error {
	if uint64(len(m)) > math.MaxUint32 {
		return fmt.Errorf("etf: map arity %d exceeds uint32", len(m))
	}
	e.w.writeByte(TagMap)
	e.w.writeUint32(uint32(len(m)))
	for _, entry := range m {
		if err := e.encodeAny(entry.Key, nil, nilAtom, depth+1); err != nil {
			return err
		}
		if err := e.encodeAny(entry.Value, nil, nilAtom, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (e *encodeState) encodeGoMap(v reflect.Value, nilAtom Atom, depth int) error {
	n := v.Len()
	if uint64(n) > math.MaxUint32 {
		return fmt.Errorf("etf: map arity %d exceeds uint32", n)
	}
	e.w.writeByte(TagMap)
	e.w.writeUint32(uint32(n))
	iter := v.MapRange()
	for iter.Next() {
		if err := e.encodeValue(iter.Key(), nil, nilAtom, depth+1); err != nil {
			return err
		}
		if err := e.encodeValue(iter.Value(), nil, nilAtom, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (e *encodeState) encodeStruct(v reflect.Value, depth int) error {
	info, err := getStructInfo(v.Type())
	if err != nil {
		return err
	}

	e.w.writeByte(TagMap)
	arityPos := len(e.w.buf)
	e.w.writeUint32(0) // patched once actual non-omitted field count is known

	var count uint32
	for _, f := range info.fields {
		fv, ok := fieldByIndexRead(v, f.index)
		if !ok {
			// Anonymous embedded pointer struct along path is nil.
			continue
		}
		if !f.required && isNilValue(fv) {
			continue
		}
		e.w.buf = append(e.w.buf, f.keyWire...)
		fieldNilAtom := e.cfg.nilAtom
		if f.hasNilAtom {
			fieldNilAtom = f.nilAtom
		}
		if err := e.encodeValue(fv, f.valSpec, fieldNilAtom, depth+1); err != nil {
			return fmt.Errorf("etf: encoding %s.%s: %w", v.Type(), f.name, err)
		}
		count++
	}

	binary.BigEndian.PutUint32(e.w.buf[arityPos:arityPos+4], count)
	return nil
}

func fieldByIndexRead(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

func isNilValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
		return v.IsNil()
	}
	return false
}
