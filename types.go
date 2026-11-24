package etf

import (
	"bytes"
	"math"
	"math/big"
)

// Marshaler is implemented by types that marshal themselves into a complete
// standalone ETF term (starting with VersionMagic 131).
// When embedded inside a container (list or map), the encoder validates and
// strips the leading 131 automatically.
type Marshaler interface {
	MarshalETF() ([]byte, error)
}

// Unmarshaler is implemented by types that unmarshal a complete standalone ETF
// term (starting with VersionMagic 131). Implementations may call etf.Unmarshal
// directly on data.
type Unmarshaler interface {
	UnmarshalETF(data []byte) error
}

// Atom represents an Erlang atom. Comparable, distinct from Binary and string.
type Atom string

// Binary represents an Erlang binary (BINARY_EXT). Comparable, safe as a map[any]any key.
type Binary string

// Bytes returns a copy of the binary contents as a byte slice.
func (b Binary) Bytes() []byte {
	return []byte(b)
}

// String returns the binary contents as a Go string.
func (b Binary) String() string {
	return string(b)
}

// Charlist represents an Erlang string/charlist (list of Unicode code points,
// encoded as NIL_EXT, STRING_EXT, or LIST_EXT).
type Charlist string

// Bitstring represents an Erlang bitstring (BIT_BINARY_EXT).
// When len(Data) > 0, Bits must be in 1..8 (number of significant high bits in the final byte).
// When len(Data) == 0, Bits must be 0.
type Bitstring struct {
	Data []byte
	Bits uint8
}

// Map represents an Erlang map supporting arbitrary keys (including non-comparable
// Erlang keys such as lists, nested maps, or mixed atom/binary/numeric keys).
type Map []MapEntry

// MapEntry is a single key-value pair in an Erlang map.
type MapEntry struct {
	Key   any
	Value any
}

// Get looks up key in the map using strict Erlang term equality (Equal).
func (m Map) Get(key any) (any, bool) {
	for _, entry := range m {
		if Equal(entry.Key, key) {
			return entry.Value, true
		}
	}
	return nil, false
}

// ImproperList represents an Erlang list with a non-NIL_EXT tail, e.g. [a, b | c].
type ImproperList struct {
	Elements []any
	Tail     any
}

// Tuple represents an Erlang tuple (SMALL_TUPLE_EXT / LARGE_TUPLE_EXT).
// Use custom Marshaler/Unmarshaler types for domain-specific tuples (such as DateTime),
// and Tuple for dynamic values, arbitrary map keys, or inside custom Marshaler/Unmarshaler code.
type Tuple []any

// MarshalETF encodes t as a standalone ETF tuple term (starting with VersionMagic 131).
func (t Tuple) MarshalETF() ([]byte, error) {
	var w wireWriter
	return encodeTerm(&w, defaultEncodeConfig(), t)
}

// UnmarshalETF decodes a standalone ETF tuple term into *t.
func (t *Tuple) UnmarshalETF(data []byte) error {
	if t == nil {
		return ErrInvalidTarget
	}
	return Unmarshal(data, t)
}

// RawTerm holds a complete standalone ETF binary (starting with VersionMagic 131)
// for deferred decoding or passthrough of any valid ETF term.
type RawTerm []byte

// MarshalETF returns r unchanged (validated by the encoder).
func (r RawTerm) MarshalETF() ([]byte, error) {
	if len(r) == 0 {
		return nil, ErrUnexpectedEOF
	}
	out := make([]byte, len(r))
	copy(out, r)
	return out, nil
}

// UnmarshalETF copies data into *r.
func (r *RawTerm) UnmarshalETF(data []byte) error {
	if r == nil {
		return ErrInvalidTarget
	}
	*r = append((*r)[:0], data...)
	return nil
}

// KeyType identifies the ETF wire type of a map key in struct tags.
type KeyType uint8

const (
	KeyAtom KeyType = iota + 1
	KeyBinary
	KeyCharlist
	KeyInteger
	KeyFloat
	KeyBoolean
)

func (k KeyType) String() string {
	switch k {
	case KeyAtom:
		return "atom"
	case KeyBinary:
		return "binary"
	case KeyCharlist:
		return "charlist"
	case KeyInteger:
		return "integer"
	case KeyFloat:
		return "float"
	case KeyBoolean:
		return "boolean"
	default:
		return "unknown"
	}
}

// Equal reports whether two decoded Go ETF values represent the exact same
// Erlang term (Erlang `=:=` semantics: strict type + value equality, unordered maps,
// integer != float, atom != binary, +0.0 != -0.0 per OTP 27).
func Equal(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Normalize booleans and Atom("true")/Atom("false") since Erlang booleans are atoms.
	a = normalizeBoolAtom(a)
	b = normalizeBoolAtom(b)

	// Normalize all integer representations (int*, uint*, *big.Int) without allocating on 64-bit fast path.
	if ai, au, ab, ak := classifyInt(a); ak != intKindNone {
		bi, bu, bb, bk := classifyInt(b)
		if ak != bk {
			return false
		}
		switch ak {
		case intKindInt64:
			return ai == bi
		case intKindUint64:
			return au == bu
		default:
			return ab.Cmp(bb) == 0
		}
	}
	if _, _, _, bk := classifyInt(b); bk != intKindNone {
		return false
	}

	// Normalize float32/float64 using exact IEEE-754 bits (OTP 27: -0.0 =/= +0.0).
	if af, aok := toFloat64(a); aok {
		bf, bok := toFloat64(b)
		if !bok {
			return false
		}
		return math.Float64bits(af) == math.Float64bits(bf)
	}
	if _, bok := toFloat64(b); bok {
		return false
	}

	switch av := a.(type) {
	case Atom:
		bv, ok := b.(Atom)
		return ok && av == bv
	case Binary:
		switch bv := b.(type) {
		case Binary:
			return av == bv
		case []byte:
			return string(av) == string(bv)
		case string:
			return string(av) == bv
		case Bitstring:
			return equalBitstringString(bv, string(av))
		default:
			return false
		}
	case []byte:
		switch bv := b.(type) {
		case []byte:
			return bytes.Equal(av, bv)
		case Binary:
			return string(av) == string(bv)
		case string:
			return string(av) == bv
		case Bitstring:
			return equalBitstringString(bv, string(av))
		default:
			return false
		}
	case string:
		switch bv := b.(type) {
		case string:
			return av == bv
		case Binary:
			return av == string(bv)
		case []byte:
			return av == string(bv)
		case Bitstring:
			return equalBitstringString(bv, av)
		default:
			return false
		}
	case Charlist:
		switch bv := b.(type) {
		case Charlist:
			return av == bv
		case []any:
			return equalCharlistSlice(av, bv)
		default:
			return false
		}
	case Bitstring:
		switch bv := b.(type) {
		case Bitstring:
			return equalBitstring(av, bv)
		case Binary:
			return equalBitstringString(av, string(bv))
		case []byte:
			return equalBitstringString(av, string(bv))
		case string:
			return equalBitstringString(av, bv)
		default:
			return false
		}
	case RawTerm:
		bv, ok := b.(RawTerm)
		return ok && bytes.Equal(av, bv)
	case []any:
		switch bv := b.(type) {
		case []any:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				if !Equal(av[i], bv[i]) {
					return false
				}
			}
			return true
		case Charlist:
			return equalCharlistSlice(bv, av)
		default:
			return false
		}
	case ImproperList:
		bv, ok := b.(ImproperList)
		if !ok || len(av.Elements) != len(bv.Elements) {
			return false
		}
		for i := range av.Elements {
			if !Equal(av.Elements[i], bv.Elements[i]) {
				return false
			}
		}
		return Equal(av.Tail, bv.Tail)
	case Tuple:
		switch bv := b.(type) {
		case Tuple:
			if len(av) != len(bv) {
				return false
			}
			for i := range av {
				if !Equal(av[i], bv[i]) {
					return false
				}
			}
			return true
		case DateTime:
			return equalDateTimeTuple(bv, av)
		default:
			return false
		}
	case DateTime:
		switch bv := b.(type) {
		case DateTime:
			return av == bv
		case Tuple:
			return equalDateTimeTuple(av, bv)
		default:
			return false
		}
	case Map:
		bv, ok := b.(Map)
		if !ok || len(av) != len(bv) {
			return false
		}
		n := len(av)
		if n == 0 {
			return true
		}
		if n <= 8 {
			var matched [8]bool
			for _, ae := range av {
				found := false
				for j, be := range bv {
					if !matched[j] && Equal(ae.Key, be.Key) && Equal(ae.Value, be.Value) {
						matched[j] = true
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
			return true
		}
		first := make(map[uint64]int, n)
		var overflow map[uint64][]int
		for j, be := range bv {
			h := hashTerm(be.Key)
			if _, exists := first[h]; !exists {
				first[h] = j
			} else {
				if overflow == nil {
					overflow = make(map[uint64][]int)
				}
				overflow[h] = append(overflow[h], j)
			}
		}
		matched := make([]bool, n)
		for _, ae := range av {
			h := hashTerm(ae.Key)
			j, ok := first[h]
			if !ok {
				return false
			}
			if !matched[j] && Equal(ae.Key, bv[j].Key) && Equal(ae.Value, bv[j].Value) {
				matched[j] = true
				continue
			}
			found := false
			for _, oj := range overflow[h] {
				if !matched[oj] && Equal(ae.Key, bv[oj].Key) && Equal(ae.Value, bv[oj].Value) {
					matched[oj] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func equalBitstring(a, b Bitstring) bool {
	if a.Bits != b.Bits || len(a.Data) != len(b.Data) {
		return false
	}
	n := len(a.Data)
	if n == 0 {
		return true
	}
	if a.Bits < 1 || a.Bits > 8 {
		return bytes.Equal(a.Data, b.Data)
	}
	if !bytes.Equal(a.Data[:n-1], b.Data[:n-1]) {
		return false
	}
	mask := byte(0xFF) << (8 - a.Bits)
	return (a.Data[n-1] & mask) == (b.Data[n-1] & mask)
}

func equalBitstringString(bs Bitstring, s string) bool {
	if len(bs.Data) == 0 {
		return bs.Bits == 0 && len(s) == 0
	}
	return bs.Bits == 8 && string(bs.Data) == s
}

func normalizeBoolAtom(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return Atom("true")
		}
		return Atom("false")
	}
	return v
}

func equalCharlistSlice(c Charlist, s []any) bool {
	runes := []rune(c)
	if len(runes) != len(s) {
		return false
	}
	for i, r := range runes {
		i64, _, _, kind := classifyInt(s[i])
		if kind != intKindInt64 || i64 != int64(r) {
			return false
		}
	}
	return true
}

const (
	intKindNone   uint8 = iota
	intKindInt64        // value in [MinInt64, MaxInt64]
	intKindUint64       // value in [MaxInt64+1, MaxUint64]
	intKindBig          // value < MinInt64 or > MaxUint64
)

func classifyInt(v any) (int64, uint64, *big.Int, uint8) {
	switch x := v.(type) {
	case int:
		return int64(x), 0, nil, intKindInt64
	case int8:
		return int64(x), 0, nil, intKindInt64
	case int16:
		return int64(x), 0, nil, intKindInt64
	case int32:
		return int64(x), 0, nil, intKindInt64
	case int64:
		return x, 0, nil, intKindInt64
	case uint:
		if uint64(x) <= math.MaxInt64 {
			return int64(x), 0, nil, intKindInt64
		}
		return 0, uint64(x), nil, intKindUint64
	case uint8:
		return int64(x), 0, nil, intKindInt64
	case uint16:
		return int64(x), 0, nil, intKindInt64
	case uint32:
		return int64(x), 0, nil, intKindInt64
	case uint64:
		if x <= math.MaxInt64 {
			return int64(x), 0, nil, intKindInt64
		}
		return 0, x, nil, intKindUint64
	case *big.Int:
		if x == nil {
			return 0, 0, nil, intKindNone
		}
		if x.IsInt64() {
			return x.Int64(), 0, nil, intKindInt64
		}
		if x.IsUint64() {
			return 0, x.Uint64(), nil, intKindUint64
		}
		return 0, 0, x, intKindBig
	case big.Int:
		if x.IsInt64() {
			return x.Int64(), 0, nil, intKindInt64
		}
		if x.IsUint64() {
			return 0, x.Uint64(), nil, intKindUint64
		}
		return 0, 0, &x, intKindBig
	default:
		return 0, 0, nil, intKindNone
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	default:
		return 0, false
	}
}

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

const (
	hashTagNil byte = iota + 1
	hashTagAtom
	hashTagInt64
	hashTagUint64
	hashTagBigInt
	hashTagFloat
	hashTagBinary
	hashTagBitstring
	hashTagList
	hashTagImproperList
	hashTagTuple
	hashTagMap
	hashTagRawTerm
	hashTagUnknown
)

func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func hashByte(h uint64, b byte) uint64 {
	h ^= uint64(b)
	return h * fnvPrime64
}

func hashUint64(h, v uint64) uint64 {
	h ^= v
	return mix64(h)
}

func hashString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return hashUint64(h, uint64(len(s)))
}

func hashBytes(h uint64, b []byte) uint64 {
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return hashUint64(h, uint64(len(b)))
}

func hashInt64Val(v int64) uint64 {
	return hashUint64(hashByte(fnvOffset64, hashTagInt64), uint64(v))
}

// hashTerm returns a canonical hash invariant under Equal (Equal(a, b) => hashTerm(a) == hashTerm(b)).
func hashTerm(v any) uint64 {
	if v == nil {
		return hashByte(fnvOffset64, hashTagNil)
	}
	v = normalizeBoolAtom(v)
	if i64, u64, bi, kind := classifyInt(v); kind != intKindNone {
		switch kind {
		case intKindInt64:
			return hashInt64Val(i64)
		case intKindUint64:
			return hashUint64(hashByte(fnvOffset64, hashTagUint64), u64)
		default:
			h := hashByte(fnvOffset64, hashTagBigInt)
			if bi.Sign() < 0 {
				h = hashByte(h, 1)
			} else {
				h = hashByte(h, 0)
			}
			return hashBytes(h, bi.Bytes())
		}
	}
	if f, ok := toFloat64(v); ok {
		return hashUint64(hashByte(fnvOffset64, hashTagFloat), math.Float64bits(f))
	}
	switch x := v.(type) {
	case Atom:
		return hashString(hashByte(fnvOffset64, hashTagAtom), string(x))
	case Binary:
		return hashString(hashByte(fnvOffset64, hashTagBinary), string(x))
	case string:
		return hashString(hashByte(fnvOffset64, hashTagBinary), x)
	case []byte:
		return hashBytes(hashByte(fnvOffset64, hashTagBinary), x)
	case Bitstring:
		n := len(x.Data)
		if n == 0 {
			if x.Bits == 0 {
				return hashBytes(hashByte(fnvOffset64, hashTagBinary), nil)
			}
			return hashByte(hashByte(fnvOffset64, hashTagBitstring), x.Bits)
		}
		if x.Bits == 8 {
			return hashBytes(hashByte(fnvOffset64, hashTagBinary), x.Data)
		}
		h := hashByte(hashByte(fnvOffset64, hashTagBitstring), x.Bits)
		if x.Bits >= 1 && x.Bits < 8 {
			for i := 0; i < n-1; i++ {
				h ^= uint64(x.Data[i])
				h *= fnvPrime64
			}
			mask := byte(0xFF) << (8 - x.Bits)
			h ^= uint64(x.Data[n-1] & mask)
			h *= fnvPrime64
			return hashUint64(h, uint64(n))
		}
		return hashBytes(h, x.Data)
	case Charlist:
		h := hashByte(fnvOffset64, hashTagList)
		var count uint64
		for _, r := range x {
			h = hashUint64(h, hashInt64Val(int64(r)))
			count++
		}
		return hashUint64(h, count)
	case []any:
		h := hashByte(fnvOffset64, hashTagList)
		for _, elem := range x {
			h = hashUint64(h, hashTerm(elem))
		}
		return hashUint64(h, uint64(len(x)))
	case ImproperList:
		h := hashByte(fnvOffset64, hashTagImproperList)
		for _, elem := range x.Elements {
			h = hashUint64(h, hashTerm(elem))
		}
		h = hashUint64(h, uint64(len(x.Elements)))
		return hashUint64(h, hashTerm(x.Tail))
	case Tuple:
		h := hashByte(fnvOffset64, hashTagTuple)
		for _, elem := range x {
			h = hashUint64(h, hashTerm(elem))
		}
		return hashUint64(h, uint64(len(x)))
	case DateTime:
		return hashDateTime(x)
	case RawTerm:
		return hashBytes(hashByte(fnvOffset64, hashTagRawTerm), x)
	case Map:
		var sum uint64
		for _, e := range x {
			hk := hashTerm(e.Key)
			hv := hashTerm(e.Value)
			sum += mix64(hk ^ mix64(hv+0x9e3779b97f4a7c15))
		}
		h := hashByte(fnvOffset64, hashTagMap)
		h = hashUint64(h, uint64(len(x)))
		return hashUint64(h, sum)
	default:
		return hashByte(fnvOffset64, hashTagUnknown)
	}
}
