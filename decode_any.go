package etf

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

func (d *decodeState) decodeAny(spec *typeSpec, nilAtom Atom, depth int) (any, error) {
	if depth > d.cfg.maxDepth {
		return nil, d.wrapErr(0, nil, ErrMaxDepthExceeded)
	}
	tag, err := d.r.peekByte()
	if err != nil {
		return nil, d.wrapErr(0, nil, err)
	}

	if spec != nil && spec.kind != valAny {
		if err := validateTagForSpec(tag, spec); err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
	}

	switch tagTable[tag] {
	case tagInteger:
		i64, bi, t, err := d.r.readAnyInteger()
		if err != nil {
			return nil, d.wrapErr(t, nil, err)
		}
		if bi != nil {
			return bi, nil
		}
		return i64, nil

	case tagFloat:
		_, _ = d.r.readByte()
		f, err := d.r.readFloat()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		return f, nil

	case tagAtom:
		_, _ = d.r.readByte()
		s, err := d.r.readAtom(tag)
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		if spec != nil && spec.kind == valBoolean {
			if s == "true" {
				return true, nil
			}
			if s == "false" {
				return false, nil
			}
			return nil, d.wrapErr(tag, nil, fmt.Errorf("%w: expected boolean atom, got %q", ErrTypeMismatch, s))
		}
		if spec != nil && spec.kind == valAtom {
			return Atom(s), nil
		}
		if s == "true" {
			return true, nil
		}
		if s == "false" {
			return false, nil
		}
		return Atom(s), nil

	case tagBinary:
		_, _ = d.r.readByte()
		s, err := d.r.readBinaryString()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		return Binary(s), nil

	case tagBitstring:
		_, _ = d.r.readByte()
		bs, err := d.r.readBitstring()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		if bs.Bits == 8 {
			return Binary(bs.Data), nil
		}
		if spec != nil && spec.kind == valBinary {
			return nil, d.wrapErr(tag, nil, fmt.Errorf("%w: non-byte-aligned bitstring (bits=%d) does not match binary", ErrTypeMismatch, bs.Bits))
		}
		return bs, nil

	case tagNil:
		_, _ = d.r.readByte()
		if spec != nil && spec.kind == valCharlist {
			return Charlist(""), nil
		}
		return []any{}, nil

	case tagString:
		_, _ = d.r.readByte()
		b, err := d.r.readStringExt()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		if spec != nil && spec.kind == valCharlist {
			return Charlist(latin1ToUTF8(b)), nil
		}
		if spec != nil && spec.kind == valList && spec.elem != nil &&
			spec.elem.kind != valAny && spec.elem.kind != valInteger && spec.elem.kind != valNumber {
			return nil, d.wrapErr(tag, nil, fmt.Errorf("%w: list of bytes (STRING_EXT) does not match %s", ErrTypeMismatch, spec.rawText))
		}
		out := make([]any, len(b))
		for i, c := range b {
			out[i] = int64(c)
		}
		return out, nil

	case tagList:
		_, _ = d.r.readByte()
		n, err := d.r.readListHeader()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		if spec != nil && spec.kind == valCharlist {
			s, err := d.readCharlistBody(n, depth)
			if err != nil {
				return nil, d.wrapErr(tag, nil, err)
			}
			return Charlist(s), nil
		}
		var elemSpec *typeSpec
		if spec != nil && spec.kind == valList {
			elemSpec = spec.elem
		}
		elems := make([]any, 0, cappedCap(int(n)))
		for i := uint32(0); i < n; i++ {
			d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
			val, err := d.decodeAny(elemSpec, nilAtom, depth+1)
			if err != nil {
				return nil, err
			}
			d.popPath()
			elems = append(elems, val)
		}
		tailTag, err := d.r.peekByte()
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		if tailTag == TagNil {
			_, _ = d.r.readByte()
			return elems, nil
		}
		d.pushPath(pathFrame{kind: pathTail})
		tail, err := d.decodeAny(nil, nilAtom, depth+1)
		if err != nil {
			return nil, err
		}
		d.popPath()
		if n == 0 {
			if spec != nil && spec.kind == valList {
				return nil, d.wrapErr(tag, nil, ErrImproperList)
			}
			return tail, nil
		}
		return ImproperList{Elements: elems, Tail: tail}, nil

	case tagTuple:
		_, _ = d.r.readByte()
		arity, err := d.r.readTupleHeader(tag)
		if err != nil {
			return nil, d.wrapErr(tag, nil, err)
		}
		elems := make(Tuple, 0, cappedCap(int(arity)))
		for i := uint32(0); i < arity; i++ {
			d.pushPath(pathFrame{kind: pathIndex, idx: int(i)})
			val, err := d.decodeAny(nil, nilAtom, depth+1)
			if err != nil {
				return nil, err
			}
			d.popPath()
			elems = append(elems, val)
		}
		if spec != nil && spec.kind == valDateTime {
			dt, err := dateTimeFromTuple(elems)
			if err != nil {
				return nil, d.wrapErr(tag, nil, err)
			}
			return dt, nil
		}
		return elems, nil

	case tagMap:
		return d.decodeETFMap(nilAtom, depth)

	default:
		return nil, d.wrapErr(tag, nil, fmt.Errorf("%w: %d", ErrUnsupportedTag, tag))
	}
}

type dupKeyTracker struct {
	scalars      map[structKey]struct{}
	complexFirst map[uint64]any
	complexMore  map[uint64][]any
}

func (t *dupKeyTracker) init(hint int) {
	t.scalars = make(map[structKey]struct{}, cappedCap(hint))
}

func (t *dupKeyTracker) addStructKey(k structKey) bool {
	if _, exists := t.scalars[k]; exists {
		return false
	}
	t.scalars[k] = struct{}{}
	return true
}

func (t *dupKeyTracker) addAny(k any) bool {
	if sk, ok := anyToStructKey(k); ok {
		return t.addStructKey(sk)
	}
	h := hashTerm(k)
	if t.complexFirst == nil {
		t.complexFirst = make(map[uint64]any)
		t.complexFirst[h] = k
		return true
	}
	first, exists := t.complexFirst[h]
	if !exists {
		t.complexFirst[h] = k
		return true
	}
	if Equal(first, k) {
		return false
	}
	for _, prev := range t.complexMore[h] {
		if Equal(prev, k) {
			return false
		}
	}
	if t.complexMore == nil {
		t.complexMore = make(map[uint64][]any)
	}
	t.complexMore[h] = append(t.complexMore[h], k)
	return true
}

func anyToStructKey(v any) (structKey, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return structKey{kind: keyKindAtom, str: "true"}, true
		}
		return structKey{kind: keyKindAtom, str: "false"}, true
	case Atom:
		return structKey{kind: keyKindAtom, str: string(x)}, true
	case Binary:
		return structKey{kind: keyKindBinary, str: string(x)}, true
	case string:
		return structKey{kind: keyKindString, str: x}, true
	case int:
		return structKey{kind: keyKindInteger, str: strconv.FormatInt(int64(x), 10)}, true
	case int8:
		return structKey{kind: keyKindInteger, str: strconv.FormatInt(int64(x), 10)}, true
	case int16:
		return structKey{kind: keyKindInteger, str: strconv.FormatInt(int64(x), 10)}, true
	case int32:
		return structKey{kind: keyKindInteger, str: strconv.FormatInt(int64(x), 10)}, true
	case int64:
		return structKey{kind: keyKindInteger, str: strconv.FormatInt(x, 10)}, true
	case uint:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(uint64(x), 10)}, true
	case uint8:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(uint64(x), 10)}, true
	case uint16:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(uint64(x), 10)}, true
	case uint32:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(uint64(x), 10)}, true
	case uint64:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(x, 10)}, true
	case uintptr:
		return structKey{kind: keyKindInteger, str: strconv.FormatUint(uint64(x), 10)}, true
	case *big.Int:
		if x != nil {
			return structKey{kind: keyKindInteger, str: x.String()}, true
		}
	case big.Int:
		return structKey{kind: keyKindInteger, str: x.String()}, true
	case float32:
		return structKey{kind: keyKindFloat, floatBits: math.Float64bits(float64(x))}, true
	case float64:
		return structKey{kind: keyKindFloat, floatBits: math.Float64bits(x)}, true
	case Charlist:
		return structKey{kind: keyKindCharlist, str: string(x)}, true
	}
	return structKey{}, false
}
