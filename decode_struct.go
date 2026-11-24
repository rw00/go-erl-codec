package etf

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
)

func (d *decodeState) decodeMapInto(v reflect.Value, spec *typeSpec, nilAtom Atom, depth int) error {
	if v.Type() == etfMapType {
		m, err := d.decodeETFMap(nilAtom, depth)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(m))
		return nil
	}

	switch v.Kind() {
	case reflect.Struct:
		return d.decodeMapIntoStruct(v, depth)
	case reflect.Map:
		return d.decodeMapIntoGoMap(v, nilAtom, depth)
	default:
		return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: cannot decode map into Go %s", ErrTypeMismatch, v.Type()))
	}
}

func (d *decodeState) decodeETFMap(nilAtom Atom, depth int) (Map, error) {
	_, _ = d.r.readByte()
	arity, err := d.r.readMapHeader()
	if err != nil {
		return nil, d.wrapErr(TagMap, etfMapType, err)
	}

	m := make(Map, 0, cappedCap(int(arity)))
	var dup dupKeyTracker
	if d.cfg.rejectDuplicateMapKeys {
		dup.init(int(arity))
	}

	for i := uint32(0); i < arity; i++ {
		d.pushPath(pathFrame{kind: pathMapKeyIdx, idx: int(i)})
		k, err := d.decodeAny(nil, nilAtom, depth+1)
		if err != nil {
			return nil, err
		}
		d.popPath()
		if d.cfg.rejectDuplicateMapKeys {
			if !dup.addAny(k) {
				return nil, d.wrapErr(TagMap, etfMapType, fmt.Errorf("%w: %v", ErrDuplicateMapKey, k))
			}
		}
		d.pushPath(pathFrame{kind: pathMapVal, key: k})
		val, err := d.decodeAny(nil, nilAtom, depth+1)
		if err != nil {
			return nil, err
		}
		d.popPath()
		m = append(m, MapEntry{Key: k, Value: val})
	}
	return m, nil
}

func (d *decodeState) decodeMapIntoGoMap(v reflect.Value, nilAtom Atom, depth int) error {
	_, _ = d.r.readByte()
	arity, err := d.r.readMapHeader()
	if err != nil {
		return d.wrapErr(TagMap, v.Type(), err)
	}

	t := v.Type()
	if v.IsNil() {
		v.Set(reflect.MakeMapWithSize(t, cappedSliceCap(int(arity), t.Key().Size()+t.Elem().Size())))
	}

	var dup dupKeyTracker
	var seenGo map[any]struct{}
	if d.cfg.rejectDuplicateMapKeys {
		dup.init(int(arity))
	}

	for i := uint32(0); i < arity; i++ {
		kStart := d.r.pos
		kElem := reflect.New(t.Key()).Elem()
		d.pushPath(pathFrame{kind: pathMapKeyIdx, idx: int(i)})
		if err := d.decodeValue(kElem, nil, nilAtom, depth+1); err != nil {
			return err
		}

		// Prevent runtime panic when decoding non-comparable Erlang keys (lists, maps, etc.) into map[any]V.
		if t.Key().Kind() == reflect.Interface && !kElem.IsNil() && !kElem.Elem().Type().Comparable() {
			return d.wrapErr(d.r.data[kStart], t.Key(), ErrUncomparableMapKey)
		}
		d.popPath()

		if d.cfg.rejectDuplicateMapKeys {
			kv := kElem.Interface()
			if sk, ok := anyToStructKey(kv); ok {
				if !dup.addStructKey(sk) {
					return d.wrapErr(TagMap, t, fmt.Errorf("%w: %v", ErrDuplicateMapKey, kv))
				}
			} else {
				if seenGo == nil {
					seenGo = make(map[any]struct{}, cappedCap(int(arity)))
				}
				if _, exists := seenGo[kv]; exists {
					return d.wrapErr(TagMap, t, fmt.Errorf("%w: %v", ErrDuplicateMapKey, kv))
				}
				seenGo[kv] = struct{}{}
			}
		}

		valElem := reflect.New(t.Elem()).Elem()
		d.pushPath(pathFrame{kind: pathMapVal, keyVal: kElem})
		if err := d.decodeValue(valElem, nil, nilAtom, depth+1); err != nil {
			return err
		}
		d.popPath()
		v.SetMapIndex(kElem, valElem)
	}
	return nil
}

func (d *decodeState) decodeMapIntoStruct(v reflect.Value, depth int) error {
	info, err := getStructInfo(v.Type())
	if err != nil {
		return err
	}

	_, _ = d.r.readByte()
	arity, err := d.r.readMapHeader()
	if err != nil {
		return d.wrapErr(TagMap, v.Type(), err)
	}

	var seenMask uint64
	var seenField []bool
	if info.hasRequired && len(info.fields) > 64 {
		seenField = make([]bool, len(info.fields))
	}

	var dup dupKeyTracker
	if d.cfg.rejectDuplicateMapKeys {
		dup.init(int(arity))
	}

	for i := uint32(0); i < arity; i++ {
		sk, isScalar, rawKeyAny, err := d.readMapKeyForStruct(info.hasCharlistKey, depth+1)
		if err != nil {
			return d.wrapErr(TagMap, v.Type(), err)
		}

		if d.cfg.rejectDuplicateMapKeys {
			if isScalar {
				if !dup.addStructKey(sk) {
					return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: %q", ErrDuplicateMapKey, sk.display()))
				}
			} else {
				if !dup.addAny(rawKeyAny) {
					return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: %v", ErrDuplicateMapKey, rawKeyAny))
				}
			}
		}

		if isScalar {
			if idx, ok := info.byKey[sk]; ok {
				if info.hasRequired {
					if seenField != nil {
						seenField[idx] = true
					} else {
						seenMask |= 1 << uint(idx)
					}
				}
				f := info.fields[idx]
				d.pushPath(pathFrame{kind: pathField, name: f.name})
				fv, err := fieldByIndexWrite(v, f.index)
				if err != nil {
					return d.wrapErr(TagMap, v.Type(), err)
				}
				fieldNilAtom := d.cfg.nilAtom
				if f.hasNilAtom {
					fieldNilAtom = f.nilAtom
				}
				if err := d.decodeValue(fv, f.valSpec, fieldNilAtom, depth+1); err != nil {
					return err
				}
				d.popPath()
				continue
			}
		}

		if d.cfg.disallowUnknownFields {
			keyDesc := fmt.Sprintf("%v", rawKeyAny)
			if isScalar {
				keyDesc = sk.display()
			}
			return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: %q in %s", ErrUnknownField, keyDesc, v.Type()))
		}
		if err := d.r.skipIgnoredTerm(depth+1, d.cfg.maxDepth); err != nil {
			return d.wrapErr(TagMap, v.Type(), err)
		}
	}

	if info.hasRequired {
		if seenField != nil {
			for i, f := range info.fields {
				if f.required && !seenField[i] {
					d.pushPath(pathFrame{kind: pathField, name: f.name})
					return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: %s (%s %q)", ErrMissingRequiredField, f.name, f.key.keyType, f.key.rawName))
				}
			}
		} else if info.requiredMask&^seenMask != 0 {
			for i, f := range info.fields {
				if f.required && seenMask&(1<<uint(i)) == 0 {
					d.pushPath(pathFrame{kind: pathField, name: f.name})
					return d.wrapErr(TagMap, v.Type(), fmt.Errorf("%w: %s (%s %q)", ErrMissingRequiredField, f.name, f.key.keyType, f.key.rawName))
				}
			}
		}
	}
	return nil
}

// readMapKeyForStruct reads a map key and returns its canonical structKey if it is
// a scalar/charlist key, or falls back to decodeAny for complex keys (nested maps/lists).
func (d *decodeState) readMapKeyForStruct(hasCharlistKey bool, depth int) (structKey, bool, any, error) {
	if depth > d.cfg.maxDepth {
		return structKey{}, false, nil, ErrMaxDepthExceeded
	}
	tag, err := d.r.peekByte()
	if err != nil {
		return structKey{}, false, nil, err
	}

	switch tagTable[tag] {
	case tagAtom:
		_, _ = d.r.readByte()
		s, err := d.r.readAtom(tag)
		if err != nil {
			return structKey{}, false, nil, err
		}
		return structKey{kind: keyKindAtom, str: s}, true, nil, nil

	case tagBinary:
		_, _ = d.r.readByte()
		s, err := d.r.readBinaryString()
		if err != nil {
			return structKey{}, false, nil, err
		}
		return structKey{kind: keyKindBinary, str: s}, true, nil, nil

	case tagBitstring:
		_, _ = d.r.readByte()
		bs, err := d.r.readBitstring()
		if err != nil {
			return structKey{}, false, nil, err
		}
		if bs.Bits == 8 {
			return structKey{kind: keyKindBinary, str: string(bs.Data)}, true, nil, nil
		}
		return structKey{}, false, bs, nil

	case tagInteger:
		i64, bi, _, err := d.r.readAnyInteger()
		if err != nil {
			return structKey{}, false, nil, err
		}
		var s string
		if bi != nil {
			s = bi.String()
		} else {
			s = strconv.FormatInt(i64, 10)
		}
		return structKey{kind: keyKindInteger, str: s}, true, nil, nil

	case tagFloat:
		_, _ = d.r.readByte()
		f, err := d.r.readFloat()
		if err != nil {
			return structKey{}, false, nil, err
		}
		return structKey{kind: keyKindFloat, floatBits: math.Float64bits(f)}, true, nil, nil

	case tagNil:
		if !hasCharlistKey {
			val, err := d.decodeAnyOrSkipUnknownKey(depth)
			return structKey{}, false, val, err
		}
		_, _ = d.r.readByte()
		return structKey{kind: keyKindCharlist, str: ""}, true, nil, nil

	case tagString:
		if !hasCharlistKey {
			val, err := d.decodeAnyOrSkipUnknownKey(depth)
			return structKey{}, false, val, err
		}
		_, _ = d.r.readByte()
		b, err := d.r.readStringExt()
		if err != nil {
			return structKey{}, false, nil, err
		}
		return structKey{kind: keyKindCharlist, str: latin1ToUTF8(b)}, true, nil, nil

	case tagList:
		if !hasCharlistKey {
			val, err := d.decodeAnyOrSkipUnknownKey(depth)
			return structKey{}, false, val, err
		}
		// Attempt charlist key match; if not a valid charlist (e.g. non-codepoint list), decode/skip as unknown key.
		saved := d.r.pos
		_, _ = d.r.readByte()
		if n, err := d.r.readListHeader(); err == nil {
			if s, cerr := d.readCharlistBody(n, depth); cerr == nil {
				return structKey{kind: keyKindCharlist, str: s}, true, nil, nil
			}
		}
		d.r.pos = saved
		val, err := d.decodeAnyOrSkipUnknownKey(depth)
		return structKey{}, false, val, err

	default:
		val, err := d.decodeAnyOrSkipUnknownKey(depth)
		return structKey{}, false, val, err
	}
}

// decodeAnyOrSkipUnknownKey decodes a non-scalar struct map key into a Go value for
// duplicate-key tracking, falling back to raw wire bytes (RawTerm) if the key contains
// an ETF term type that the codec does not materialize (e.g. NEW_PID_EXT, NEWER_REFERENCE_EXT).
func (d *decodeState) decodeAnyOrSkipUnknownKey(depth int) (any, error) {
	savedPos := d.r.pos
	savedPathLen := len(d.path)
	val, err := d.decodeAny(nil, d.cfg.nilAtom, depth)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, ErrUnsupportedTag) {
		return nil, err
	}
	d.r.pos = savedPos
	d.path = d.path[:savedPathLen]
	if skipErr := d.r.skipIgnoredTerm(depth, d.cfg.maxDepth); skipErr != nil {
		return nil, skipErr
	}
	return RawTerm(d.r.data[savedPos:d.r.pos]), nil
}

func fieldByIndexWrite(v reflect.Value, index []int) (reflect.Value, error) {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, fmt.Errorf("etf: cannot set embedded pointer to unexported struct: %s", v.Type().Elem())
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, nil
}
