package etf

import (
	"errors"
	"math"
	"math/big"
	"reflect"
	"testing"
)

func TestNumericCoercion(t *testing.T) {
	// Integer -> all Go numeric types.
	raw, err := Marshal(int64(42))
	if err != nil {
		t.Fatal(err)
	}

	var i8 int8
	var i32 int32
	var i64 int64
	var u16 uint16
	var u64 uint64
	var f32 float32
	var f64 float64
	var bi *big.Int

	for _, target := range []any{&i8, &i32, &i64, &u16, &u64, &f32, &f64, &bi} {
		if err := Unmarshal(raw, target); err != nil {
			t.Fatalf("coercing 42 into %T failed: %v", target, err)
		}
	}
	if i8 != 42 || i32 != 42 || i64 != 42 || u16 != 42 || u64 != 42 || f32 != 42 || f64 != 42 || bi.Int64() != 42 {
		t.Fatalf("unexpected coerced values")
	}

	// Overflow checks.
	raw300, _ := Marshal(300)
	if err := Unmarshal(raw300, &i8); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow for 300 -> int8, got %v", err)
	}
	rawNeg, _ := Marshal(-1)
	if err := Unmarshal(rawNeg, &u16); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow for -1 -> uint16, got %v", err)
	}

	// Float -> integer: lossless ok, lossy errors.
	rawExact, _ := Marshal(42.0)
	var dstInt int
	if err := Unmarshal(rawExact, &dstInt); err != nil || dstInt != 42 {
		t.Fatalf("expected lossless 42.0 -> 42, got %d err=%v", dstInt, err)
	}

	rawFrac, _ := Marshal(42.5)
	if err := Unmarshal(rawFrac, &dstInt); !errors.Is(err, ErrLossyCoercion) {
		t.Fatalf("expected ErrLossyCoercion for 42.5 -> int, got %v", err)
	}

	// 2^63 as float64 overflows int64!
	rawOverflowFloat, _ := Marshal(math.Ldexp(1, 63))
	if err := Unmarshal(rawOverflowFloat, &i64); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow for 2^63 -> int64, got %v", err)
	}

	// Strict mode disables int -> float and float -> int coercion.
	if err := Unmarshal(raw, &f64, WithStrictTypes()); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch in strict mode for int -> float64, got %v", err)
	}
	if err := Unmarshal(rawExact, &dstInt, WithStrictTypes()); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch in strict mode for float -> int, got %v", err)
	}
}

func TestStringExtCoercionToSlicesAndStrings(t *testing.T) {
	// STRING_EXT [1, 2, 200] (bytes 0..255).
	raw := []byte{VersionMagic, TagString, 0, 3, 1, 2, 200}

	var ints []int
	if err := Unmarshal(raw, &ints); err != nil {
		t.Fatalf("STRING_EXT -> []int failed: %v", err)
	}
	if !reflect.DeepEqual(ints, []int{1, 2, 200}) {
		t.Fatalf("got %v", ints)
	}

	var floats []float64
	if err := Unmarshal(raw, &floats); err != nil {
		t.Fatalf("STRING_EXT -> []float64 failed: %v", err)
	}
	if !reflect.DeepEqual(floats, []float64{1, 2, 200}) {
		t.Fatalf("got %v", floats)
	}

	// int8 must reject 200 with ErrOverflow!
	var i8s []int8
	if err := Unmarshal(raw, &i8s); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow for 200 -> []int8, got %v", err)
	}

	// Latin-1 STRING_EXT -> string ("hé").
	rawLatin := []byte{VersionMagic, TagString, 0, 2, 'h', 0xE9}
	var s string
	if err := Unmarshal(rawLatin, &s); err != nil || s != "hé" {
		t.Fatalf("expected hé, got %q err=%v", s, err)
	}

	// STRING_EXT -> any produces []any of int64.
	var anyVal any
	if err := Unmarshal(raw, &anyVal); err != nil {
		t.Fatalf("STRING_EXT -> any failed: %v", err)
	}
	if !Equal(anyVal, []any{int64(1), int64(2), int64(200)}) {
		t.Fatalf("unexpected any value: %#v", anyVal)
	}

	// STRING_EXT -> arrays of any, *big.Int, multi-level pointers, and Unmarshaler.
	var arrAny [3]any
	if err := Unmarshal(raw, &arrAny); err != nil {
		t.Fatalf("STRING_EXT -> [3]any failed: %v", err)
	}
	if !Equal(arrAny[:], []any{int64(1), int64(2), int64(200)}) {
		t.Fatalf("unexpected [3]any: %#v", arrAny)
	}

	var arrBig [3]*big.Int
	if err := Unmarshal(raw, &arrBig); err != nil {
		t.Fatalf("STRING_EXT -> [3]*big.Int failed: %v", err)
	}
	if arrBig[0].Int64() != 1 || arrBig[1].Int64() != 2 || arrBig[2].Int64() != 200 {
		t.Fatalf("unexpected [3]*big.Int: %v", arrBig)
	}

	var slicePP []**int
	if err := Unmarshal(raw, &slicePP); err != nil {
		t.Fatalf("STRING_EXT -> []**int failed: %v", err)
	}
	if len(slicePP) != 3 || **slicePP[2] != 200 {
		t.Fatalf("unexpected []**int: %v", slicePP)
	}

	var arrRaw [3]RawTerm
	if err := Unmarshal(raw, &arrRaw); err != nil {
		t.Fatalf("STRING_EXT -> [3]RawTerm failed: %v", err)
	}
	var third int
	if err := Unmarshal(arrRaw[2], &third); err != nil || third != 200 {
		t.Fatalf("expected RawTerm element 2 to decode as 200, got %d err=%v", third, err)
	}
}

func TestExplicitWrapperTypesAndStrictMode(t *testing.T) {
	atomRaw, _ := Marshal(Atom("hello"))
	binRaw, _ := Marshal(Binary("hello"))
	nilRaw := []byte{VersionMagic, TagNil}
	strExtRaw := []byte{VersionMagic, TagString, 0, 2, 'h', 'i'}
	listRaw, _ := Marshal([]int{65, 66})
	bitAlignedRaw := []byte{VersionMagic, TagBitBinary, 0, 0, 0, 2, 8, 'o', 'k'}

	// Explicit wrapper types never coerce across wire types, even in default mode.
	var a Atom
	var b Binary
	var c Charlist
	for _, tc := range []struct {
		name string
		raw  []byte
		dst  any
	}{
		{"atom_into_binary", atomRaw, &b},
		{"atom_into_charlist", atomRaw, &c},
		{"binary_into_atom", binRaw, &a},
		{"binary_into_charlist", binRaw, &c},
		{"bitstring_into_atom", bitAlignedRaw, &a},
		{"bitstring_into_charlist", bitAlignedRaw, &c},
		{"nil_into_atom", nilRaw, &a},
		{"nil_into_binary", nilRaw, &b},
		{"stringext_into_atom", strExtRaw, &a},
		{"stringext_into_binary", strExtRaw, &b},
		{"list_into_atom", listRaw, &a},
		{"list_into_binary", listRaw, &b},
	} {
		if err := Unmarshal(tc.raw, tc.dst); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("%s: expected ErrTypeMismatch, got %v", tc.name, err)
		}
	}

	// WithStrictTypes rejects cross-type coercion into plain string / []byte.
	var s string
	var bs []byte
	for _, tc := range []struct {
		name string
		raw  []byte
		dst  any
	}{
		{"strict_atom_to_string", atomRaw, &s},
		{"strict_atom_to_bytes", atomRaw, &bs},
		{"strict_nil_to_string", nilRaw, &s},
		{"strict_nil_to_bytes", nilRaw, &bs},
		{"strict_stringext_to_string", strExtRaw, &s},
		{"strict_stringext_to_bytes", strExtRaw, &bs},
		{"strict_list_to_string", listRaw, &s},
	} {
		if err := Unmarshal(tc.raw, tc.dst, WithStrictTypes()); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("%s: expected ErrTypeMismatch in strict mode, got %v", tc.name, err)
		}
	}

	// Strict mode still allows exact matching wrapper types.
	if err := Unmarshal(atomRaw, &a, WithStrictTypes()); err != nil || a != "hello" {
		t.Fatalf("strict atom -> Atom failed: %q %v", a, err)
	}
	if err := Unmarshal(binRaw, &b, WithStrictTypes()); err != nil || b != "hello" {
		t.Fatalf("strict binary -> Binary failed: %q %v", b, err)
	}
	if err := Unmarshal(strExtRaw, &c, WithStrictTypes()); err != nil || c != "hi" {
		t.Fatalf("strict STRING_EXT -> Charlist failed: %q %v", c, err)
	}
	if err := Unmarshal(nilRaw, &c, WithStrictTypes()); err != nil || c != "" {
		t.Fatalf("strict NIL_EXT -> Charlist failed: %q %v", c, err)
	}

	// LIST_EXT decoding into string must validate list(T) element spec.
	type BadListString struct {
		S string `etf:"s:atom => list(atom)"`
	}
	listMapRaw, _ := Marshal(Map{{Key: Atom("s"), Value: []int{65, 66}}})
	var bls BadListString
	if err := Unmarshal(listMapRaw, &bls); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for LIST_EXT integers into list(atom) string, got %v", err)
	}

	// Invalid runes in []rune charlist encoding must return ErrInvalidUTF8.
	type BadRunes struct {
		R []rune `etf:"r:atom => charlist"`
	}
	if _, err := Marshal(BadRunes{R: []rune{'a', -1}}); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for invalid rune -1, got %v", err)
	}
	if _, err := Marshal(BadRunes{R: []rune{0xD800}}); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for surrogate rune, got %v", err)
	}
}
