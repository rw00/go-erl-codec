package etf

import (
	"bytes"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"
)

type taggedTuple struct {
	Tag   Atom
	Value int
}

func (tt taggedTuple) MarshalETF() ([]byte, error) {
	return Marshal(Tuple{tt.Tag, tt.Value})
}

func (tt *taggedTuple) UnmarshalETF(data []byte) error {
	var t Tuple
	if err := Unmarshal(data, &t); err != nil {
		return err
	}
	if len(t) != 2 {
		return ErrTypeMismatch
	}
	tag, ok1 := t[0].(Atom)
	val, ok2 := t[1].(int64)
	if !ok1 || !ok2 {
		return ErrTypeMismatch
	}
	*tt = taggedTuple{Tag: tag, Value: int(val)}
	return nil
}

type ptrOnlyMarshalerTuple struct {
	V int
}

func (p *ptrOnlyMarshalerTuple) MarshalETF() ([]byte, error) {
	return Marshal(Tuple{Atom("ptr"), p.V})
}

func (p *ptrOnlyMarshalerTuple) UnmarshalETF(data []byte) error {
	var t Tuple
	if err := Unmarshal(data, &t); err != nil {
		return err
	}
	if len(t) != 2 {
		return ErrTypeMismatch
	}
	i, ok := t[1].(int64)
	if !ok {
		return ErrTypeMismatch
	}
	p.V = int(i)
	return nil
}

type onlyMarshaler struct{}

func (onlyMarshaler) MarshalETF() ([]byte, error) { return Marshal(Tuple{}) }

type onlyUnmarshaler struct{}

func (*onlyUnmarshaler) UnmarshalETF([]byte) error { return nil }

type badMarshalerEmitsList struct{}

func (badMarshalerEmitsList) MarshalETF() ([]byte, error) { return Marshal([]int{1, 2}) }

func (*badMarshalerEmitsList) UnmarshalETF([]byte) error { return nil }

func TestTupleWireEncodingDecodingAndDynamic(t *testing.T) {
	// Empty tuple (nil and empty slice both encode as SMALL_TUPLE_EXT arity 0).
	for _, in := range []Tuple{nil, {}} {
		raw, err := Marshal(in)
		if err != nil {
			t.Fatalf("Marshal empty tuple failed: %v", err)
		}
		if !bytes.Equal(raw, []byte{VersionMagic, TagSmallTuple, 0}) {
			t.Fatalf("unexpected wire bytes for empty tuple: %v", raw)
		}
		var out Tuple
		if err := Unmarshal(raw, &out); err != nil || len(out) != 0 {
			t.Fatalf("Unmarshal empty tuple failed: out=%#v err=%v", out, err)
		}
		var anyOut any
		if err := Unmarshal(raw, &anyOut); err != nil || !Equal(Tuple{}, anyOut) {
			t.Fatalf("Unmarshal empty tuple into any failed: out=%#v err=%v", anyOut, err)
		}
	}

	// Small nested tuple.
	nested := Tuple{
		Atom("ok"),
		int64(42),
		Binary("payload"),
		Tuple{true, []any{int64(1), int64(2)}},
		Map{{Key: Atom("k"), Value: Binary("v")}},
	}
	rawSmall, err := Marshal(nested)
	if err != nil {
		t.Fatalf("Marshal nested tuple failed: %v", err)
	}
	if rawSmall[1] != TagSmallTuple || rawSmall[2] != 5 {
		t.Fatalf("expected SMALL_TUPLE_EXT arity 5, got tag=%d arity=%d", rawSmall[1], rawSmall[2])
	}
	var gotAny any
	if err := Unmarshal(rawSmall, &gotAny); err != nil {
		t.Fatalf("Unmarshal nested tuple into any failed: %v", err)
	}
	if !Equal(nested, gotAny) {
		t.Fatalf("nested tuple mismatch:\nwant=%#v\ngot =%#v", nested, gotAny)
	}

	// Large tuple (> 255 elements -> LARGE_TUPLE_EXT).
	large := make(Tuple, 300)
	for i := range large {
		large[i] = int64(i)
	}
	rawLarge, err := Marshal(large)
	if err != nil {
		t.Fatalf("Marshal large tuple failed: %v", err)
	}
	if rawLarge[1] != TagLargeTuple {
		t.Fatalf("expected LARGE_TUPLE_EXT (105), got %d", rawLarge[1])
	}
	var gotLarge Tuple
	if err := Unmarshal(rawLarge, &gotLarge); err != nil {
		t.Fatalf("Unmarshal large tuple failed: %v", err)
	}
	if !Equal(large, gotLarge) {
		t.Fatalf("large tuple round-trip mismatch")
	}

	// Wire LARGE_TUPLE_EXT with small arity (valid per ETF spec).
	wireLargeSmall := []byte{VersionMagic, TagLargeTuple, 0, 0, 0, 2, TagSmallInt, 10, TagSmallInt, 20}
	var gotLS Tuple
	if err := Unmarshal(wireLargeSmall, &gotLS); err != nil || !Equal(gotLS, Tuple{10, 20}) {
		t.Fatalf("Unmarshal LARGE_TUPLE_EXT(2) failed: got=%#v err=%v", gotLS, err)
	}

	// Direct Tuple.MarshalETF / (*Tuple).UnmarshalETF calls.
	directRaw, err := nested.MarshalETF()
	if err != nil {
		t.Fatalf("Tuple.MarshalETF failed: %v", err)
	}
	var directOut Tuple
	if err := (&directOut).UnmarshalETF(directRaw); err != nil || !Equal(nested, directOut) {
		t.Fatalf("Tuple.UnmarshalETF failed: got=%#v err=%v", directOut, err)
	}
	var nilTuplePtr *Tuple
	if err := nilTuplePtr.UnmarshalETF(directRaw); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget for nil *Tuple, got %v", err)
	}

	// Tuple cannot decode into non-Unmarshaler Go types (slice, array, struct, scalar).
	var sSlice []any
	if err := Unmarshal(rawSmall, &sSlice); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch decoding tuple into []any, got %v", err)
	}
	var plainStruct struct {
		A int `etf:"a:atom"`
	}
	if err := Unmarshal(rawSmall, &plainStruct); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch decoding tuple into struct, got %v", err)
	}

	// Non-tuple ETF terms cannot decode into etf.Tuple (including NIL_EXT, STRING_EXT, LIST_EXT).
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"nil_ext", []byte{VersionMagic, TagNil}},
		{"string_ext", []byte{VersionMagic, TagString, 0, 2, 'a', 'b'}},
		{"list_ext", []byte{VersionMagic, TagList, 0, 0, 0, 1, TagSmallInt, 1, TagNil}},
		{"int", []byte{VersionMagic, TagSmallInt, 7}},
		{"map", []byte{VersionMagic, TagMap, 0, 0, 0, 0}},
	} {
		var dst Tuple
		if err := Unmarshal(tc.raw, &dst); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("Unmarshal %s into Tuple: expected ErrTypeMismatch, got %v", tc.name, err)
		}
	}

	// Tuple != list in Equal; Tuple works as Map key with duplicate detection and uncomparable Go map protection.
	if Equal(Tuple{int64(1)}, []any{int64(1)}) {
		t.Fatal("Tuple must not Equal []any")
	}
	m := Map{
		{Key: Tuple{Atom("ok"), int64(1)}, Value: Binary("one")},
		{Key: Tuple{Atom("err"), Binary("x")}, Value: int64(2)},
	}
	rawMap, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal Map with Tuple keys failed: %v", err)
	}
	var gotMap Map
	if err := Unmarshal(rawMap, &gotMap); err != nil || !Equal(m, gotMap) {
		t.Fatalf("Map with Tuple keys round-trip failed: got=%#v err=%v", gotMap, err)
	}
	if v, ok := gotMap.Get(Tuple{Atom("ok"), 1}); !ok || !Equal(v, Binary("one")) {
		t.Fatalf("Map.Get with Tuple key failed: v=%#v ok=%v", v, ok)
	}
	var goAnyMap map[any]any
	if err := Unmarshal(rawMap, &goAnyMap); !errors.Is(err, ErrUncomparableMapKey) {
		t.Fatalf("expected ErrUncomparableMapKey for Tuple key in map[any]any, got %v", err)
	}

	dupTupleMap, _ := Marshal(Map{
		{Key: Tuple{Atom("k"), int64(1)}, Value: 1},
		{Key: Tuple{Atom("k"), int64(1)}, Value: 2},
	})
	var dupOut Map
	if err := Unmarshal(dupTupleMap, &dupOut); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for duplicate Tuple keys, got %v", err)
	}

	// Streaming Encoder / Decoder round-trip with small and large tuples.
	var stream bytes.Buffer
	enc := NewEncoder(&stream)
	if err := enc.Encode(nested); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(large); err != nil {
		t.Fatal(err)
	}
	dec := NewDecoder(&stream)
	var s1, s2 any
	if err := dec.Decode(&s1); err != nil || !Equal(nested, s1) {
		t.Fatalf("stream decode nested tuple failed: err=%v", err)
	}
	if err := dec.Decode(&s2); err != nil || !Equal(large, s2) {
		t.Fatalf("stream decode large tuple failed: err=%v", err)
	}

	// Safety limits: huge tuple arity and max depth.
	maliciousTuple := []byte{VersionMagic, TagLargeTuple, 0xFF, 0xFF, 0xFF, 0xFF}
	if err := Unmarshal(maliciousTuple, &gotAny); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF for huge tuple arity, got %v", err)
	}
	if err := NewDecoder(bytes.NewReader(maliciousTuple), WithMaxUncompressedSize(1024)).Decode(&gotAny); !errors.Is(err, ErrMaxSizeExceeded) {
		t.Fatalf("expected ErrMaxSizeExceeded in Decoder for huge tuple arity, got %v", err)
	}
	deepTuple := Tuple{Tuple{Tuple{Tuple{int64(1)}}}}
	if _, err := Marshal(deepTuple, WithMaxDepth(2)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded on Marshal deep tuple, got %v", err)
	}
	rawDeep, err := Marshal(deepTuple)
	if err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(rawDeep, &gotAny, WithMaxDecodeDepth(2)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded on Unmarshal deep tuple, got %v", err)
	}
}

func TestTupleStructTagCustomMarshalerEnforcement(t *testing.T) {
	// Valid: custom Marshaler/Unmarshaler, Tuple, RawTerm, DateTime, and lists thereof.
	type ValidTupleStruct struct {
		Custom  taggedTuple   `etf:"custom:atom => tuple, required"`
		Opt     *taggedTuple  `etf:"opt:atom => tuple, required"`
		Dyn     Tuple         `etf:"dyn:atom => tuple, required"`
		Raw     RawTerm       `etf:"raw:atom => tuple, required"`
		Items   []taggedTuple `etf:"items:atom => list(tuple), required"`
		Matrix  [][]Tuple     `etf:"matrix:atom => list(list(tuple))"`
		RawList RawTerm       `etf:"raw_list:atom => list(tuple)"`
	}

	rawTup, _ := Marshal(Tuple{Atom("raw"), int64(9)})
	rawTupList, _ := Marshal([]Tuple{{Atom("a")}, {Atom("b")}})
	in := ValidTupleStruct{
		Custom:  taggedTuple{Tag: "ok", Value: 7},
		Opt:     nil,
		Dyn:     Tuple{Atom("pair"), int64(1)},
		Raw:     rawTup,
		Items:   []taggedTuple{{Tag: "a", Value: 1}, {Tag: "b", Value: 2}},
		Matrix:  [][]Tuple{{{int64(1)}, {int64(2)}}},
		RawList: rawTupList,
	}
	encoded, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal ValidTupleStruct failed: %v", err)
	}
	var out ValidTupleStruct
	if err := Unmarshal(encoded, &out); err != nil {
		t.Fatalf("Unmarshal ValidTupleStruct failed: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("ValidTupleStruct round-trip mismatch:\nin =%#v\nout=%#v", in, out)
	}

	// Reject plain types and half-implemented codecs on `=> tuple` / `=> list(tuple)` / `=> datetime` with *InvalidTagError.
	invalidStructs := []struct {
		name string
		val  any
	}{
		{"plain_struct", struct {
			F struct{ X int } `etf:"f:atom => tuple"`
		}{}},
		{"slice_any", struct {
			F []any `etf:"f:atom => tuple"`
		}{}},
		{"array_int", struct {
			F [2]int `etf:"f:atom => tuple"`
		}{}},
		{"scalar_int", struct {
			F int `etf:"f:atom => tuple"`
		}{}},
		{"empty_interface", struct {
			F any `etf:"f:atom => tuple"`
		}{}},
		{"only_marshaler", struct {
			F onlyMarshaler `etf:"f:atom => tuple"`
		}{}},
		{"only_unmarshaler", struct {
			F onlyUnmarshaler `etf:"f:atom => tuple"`
		}{}},
		{"list_of_plain_ints", struct {
			F []int `etf:"f:atom => list(tuple)"`
		}{}},
		{"nested_list_of_plain_structs", struct {
			F [][]struct{ X int } `etf:"f:atom => list(list(tuple))"`
		}{}},
		{"any_for_list_tuple", struct {
			F any `etf:"f:atom => list(tuple)"`
		}{}},
		{"string_for_datetime", struct {
			F string `etf:"f:atom => datetime"`
		}{}},
		{"std_time_for_datetime", struct {
			F time.Time `etf:"f:atom => datetime"`
		}{}},
		{"any_for_datetime", struct {
			F any `etf:"f:atom => datetime"`
		}{}},
		{"list_std_time_for_datetime", struct {
			F []time.Time `etf:"f:atom => list(datetime)"`
		}{}},
	}

	for _, tc := range invalidStructs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Marshal(tc.val)
			var ite *InvalidTagError
			if !errors.As(err, &ite) {
				t.Fatalf("expected *InvalidTagError for %s, got %T: %v", tc.name, err, err)
			}
		})
	}

	// Runtime enforcement: RawTerm or bad custom Marshaler emitting non-tuple on `=> tuple` fails with ErrTypeMismatch.
	type BadEmit struct {
		B badMarshalerEmitsList `etf:"b:atom => tuple, required"`
	}
	if _, err := Marshal(BadEmit{}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch when custom Marshaler emits list for => tuple, got %v", err)
	}

	type RawTupleHolder struct {
		R RawTerm `etf:"r:atom => tuple, required"`
	}
	intRaw, _ := Marshal(int64(5))
	if _, err := Marshal(RawTupleHolder{R: intRaw}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch marshaling int RawTerm as => tuple, got %v", err)
	}
	notTupleMap, _ := Marshal(Map{{Key: Atom("r"), Value: int64(5)}})
	var rth RawTupleHolder
	if err := Unmarshal(notTupleMap, &rth); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch unmarshaling int into RawTerm => tuple, got %v", err)
	}

	// Tuple cannot be encoded as => list.
	type TupleAsList struct {
		T Tuple `etf:"t:atom => list, required"`
	}
	if _, err := Marshal(TupleAsList{T: Tuple{1}}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch encoding Tuple as => list, got %v", err)
	}

	// Pointer-receiver MarshalETF works on pointer struct (&s) and rejects non-addressable value struct (s).
	type WithPtrMarshaler struct {
		P ptrOnlyMarshalerTuple `etf:"p:atom => tuple, required"`
	}
	sVal := WithPtrMarshaler{P: ptrOnlyMarshalerTuple{V: 11}}
	if _, err := Marshal(sVal); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for non-addressable ptr-receiver Marshaler, got %v", err)
	}
	rawPtr, err := Marshal(&sVal)
	if err != nil {
		t.Fatalf("Marshal(&sVal) failed: %v", err)
	}
	var sOut WithPtrMarshaler
	if err := Unmarshal(rawPtr, &sOut); err != nil || sOut.P.V != 11 {
		t.Fatalf("Unmarshal WithPtrMarshaler failed: out=%+v err=%v", sOut, err)
	}
}

func TestDateTimeRoundTripAndHelpers(t *testing.T) {
	// Verify exact canonical wire bytes for {{2026, 11, 24}, {19, 50, 41}}.
	dt := DateTime{Year: 2026, Month: 11, Day: 24, Hour: 19, Minute: 50, Second: 41}
	wantWire := []byte{
		VersionMagic,
		TagSmallTuple, 2,
		TagSmallTuple, 3, TagInt, 0, 0, 0x07, 0xEA, TagSmallInt, 11, TagSmallInt, 24,
		TagSmallTuple, 3, TagSmallInt, 19, TagSmallInt, 50, TagSmallInt, 41,
	}
	raw, err := Marshal(dt)
	if err != nil {
		t.Fatalf("Marshal DateTime failed: %v", err)
	}
	if !bytes.Equal(raw, wantWire) {
		t.Fatalf("DateTime wire mismatch:\nwant=%v\ngot =%v", wantWire, raw)
	}
	if dt.String() != "2026-11-24 19:50:41" {
		t.Fatalf("unexpected String(): %q", dt.String())
	}

	// Round-trip valid boundary dates including leap years (0, 2000, 2024) and month ends.
	validCases := []DateTime{
		{0, 1, 1, 0, 0, 0},
		{0, 2, 29, 0, 0, 0},
		{1970, 1, 1, 0, 0, 0},
		{2000, 2, 29, 23, 59, 59},
		{2024, 2, 29, 12, 30, 0},
		{2025, 2, 28, 23, 59, 59},
		{2026, 4, 30, 10, 20, 30},
		{2026, 12, 31, 23, 59, 59},
		{10000, 1, 1, 0, 0, 0},
	}
	for _, tc := range validCases {
		b, err := Marshal(tc)
		if err != nil {
			t.Fatalf("Marshal(%v) failed: %v", tc, err)
		}
		var got DateTime
		if err := Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal(%v) failed: %v", tc, err)
		}
		if got != tc {
			t.Fatalf("DateTime mismatch: want=%+v got=%+v", tc, got)
		}
	}

	// LARGE_TUPLE_EXT encoding for outer/inner tuples also decodes cleanly.
	wireLargeDT := []byte{
		VersionMagic,
		TagLargeTuple, 0, 0, 0, 2,
		TagLargeTuple, 0, 0, 0, 3, TagInt, 0, 0, 0x07, 0xEA, TagSmallInt, 11, TagSmallInt, 24,
		TagSmallTuple, 3, TagSmallInt, 19, TagSmallInt, 50, TagSmallInt, 41,
	}
	var gotLargeDT DateTime
	if err := Unmarshal(wireLargeDT, &gotLargeDT); err != nil || gotLargeDT != dt {
		t.Fatalf("Unmarshal LARGE_TUPLE_EXT DateTime failed: got=%+v err=%v", gotLargeDT, err)
	}

	// DateTimeFromTime & Time() helpers.
	stdT := time.Date(2026, time.November, 24, 19, 50, 41, 123456, time.UTC)
	fromT := DateTimeFromTime(stdT)
	if fromT != dt {
		t.Fatalf("DateTimeFromTime mismatch: want=%+v got=%+v", dt, fromT)
	}
	backT, err := fromT.Time(nil)
	if err != nil || !backT.Equal(time.Date(2026, time.November, 24, 19, 50, 41, 0, time.UTC)) {
		t.Fatalf("DateTime.Time(nil) mismatch: got=%v err=%v", backT, err)
	}

	// Equal and hashTerm symmetry between DateTime and Tuple.
	dtTuple := Tuple{Tuple{int64(2026), int64(11), int64(24)}, Tuple{int64(19), int64(50), int64(41)}}
	if !Equal(dt, dtTuple) || !Equal(dtTuple, dt) || !Equal(dt, dt) {
		t.Fatal("Equal symmetry failed between DateTime and Tuple")
	}
	if hashTerm(dt) != hashTerm(dtTuple) {
		t.Fatal("hashTerm mismatch between Equal DateTime and Tuple")
	}

	// Struct fields with `=> datetime`, `*DateTime`, `=> tuple`, untyped, `list(datetime)`, `RawTerm`, `Tuple`.
	type Event struct {
		CreatedAt DateTime   `etf:"created_at:atom => datetime, required"`
		UpdatedAt *DateTime  `etf:"updated_at:atom => datetime, required"`
		AsTuple   DateTime   `etf:"as_tuple:atom => tuple, required"`
		Untyped   DateTime   `etf:"untyped:atom, required"`
		History   []DateTime `etf:"history:atom => list(datetime), required"`
		RawDT     RawTerm    `etf:"raw_dt:atom => datetime, required"`
		TupleDT   Tuple      `etf:"tuple_dt:atom => datetime, required"`
	}
	ev := Event{
		CreatedAt: dt,
		UpdatedAt: nil,
		AsTuple:   DateTime{2024, 2, 29, 0, 0, 0},
		Untyped:   DateTime{1970, 1, 1, 0, 0, 0},
		History:   []DateTime{dt, {2025, 1, 1, 0, 0, 0}},
		RawDT:     raw,
		TupleDT:   dtTuple,
	}
	evRaw, err := Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal Event failed: %v", err)
	}
	var evOut Event
	if err := Unmarshal(evRaw, &evOut); err != nil {
		t.Fatalf("Unmarshal Event failed: %v", err)
	}
	if !reflect.DeepEqual(ev, evOut) {
		t.Fatalf("Event round-trip mismatch:\nin =%#v\nout=%#v", ev, evOut)
	}
}

func TestDateTimeValidationErrors(t *testing.T) {
	// Invalid DateTime entries rejected on Validate, Time, Marshal, and Unmarshal.
	invalidEntries := []struct {
		name string
		dt   DateTime
	}{
		{"zero_value", DateTime{}},
		{"negative_year", DateTime{-1, 1, 1, 0, 0, 0}},
		{"month_0", DateTime{2026, 0, 1, 0, 0, 0}},
		{"month_13", DateTime{2026, 13, 1, 0, 0, 0}},
		{"month_neg", DateTime{2026, -2, 1, 0, 0, 0}},
		{"day_0", DateTime{2026, 1, 0, 0, 0, 0}},
		{"day_32", DateTime{2026, 1, 32, 0, 0, 0}},
		{"apr_31", DateTime{2026, 4, 31, 0, 0, 0}},
		{"jun_31", DateTime{2026, 6, 31, 0, 0, 0}},
		{"sep_31", DateTime{2026, 9, 31, 0, 0, 0}},
		{"nov_31", DateTime{2026, 11, 31, 0, 0, 0}},
		{"non_leap_1900_feb_29", DateTime{1900, 2, 29, 0, 0, 0}},
		{"non_leap_2025_feb_29", DateTime{2025, 2, 29, 0, 0, 0}},
		{"non_leap_2026_feb_29", DateTime{2026, 2, 29, 0, 0, 0}},
		{"non_leap_2100_feb_29", DateTime{2100, 2, 29, 0, 0, 0}},
		{"leap_2024_feb_30", DateTime{2024, 2, 30, 0, 0, 0}},
		{"leap_2000_feb_30", DateTime{2000, 2, 30, 0, 0, 0}},
		{"hour_neg", DateTime{2026, 1, 1, -1, 0, 0}},
		{"hour_24", DateTime{2026, 1, 1, 24, 0, 0}},
		{"minute_neg", DateTime{2026, 1, 1, 0, -1, 0}},
		{"minute_60", DateTime{2026, 1, 1, 0, 60, 0}},
		{"second_neg", DateTime{2026, 1, 1, 0, 0, -1}},
		{"second_60", DateTime{2026, 1, 1, 0, 0, 60}},
	}

	type RawAndTupleDT struct {
		Raw RawTerm `etf:"raw:atom => datetime"`
		Tup Tuple   `etf:"tup:atom => datetime"`
	}

	for _, tc := range invalidEntries {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.dt.Validate(); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Validate: expected ErrInvalidDateTime, got %v", err)
			}
			if _, err := tc.dt.Time(time.UTC); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Time: expected ErrInvalidDateTime, got %v", err)
			}
			if _, err := Marshal(tc.dt); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Marshal: expected ErrInvalidDateTime, got %v", err)
			}

			// Encode the invalid tuple manually and verify Unmarshal rejects it without mutating target.
			badTuple := Tuple{
				Tuple{tc.dt.Year, tc.dt.Month, tc.dt.Day},
				Tuple{tc.dt.Hour, tc.dt.Minute, tc.dt.Second},
			}
			badWire, err := Marshal(badTuple)
			if err != nil {
				t.Fatalf("Marshal badTuple failed: %v", err)
			}
			orig := DateTime{2026, 1, 1, 1, 1, 1}
			dst := orig
			if err := Unmarshal(badWire, &dst); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Unmarshal: expected ErrInvalidDateTime, got %v", err)
			}
			if dst != orig {
				t.Fatalf("failed Unmarshal mutated dst: %+v", dst)
			}

			// RawTerm and Tuple with `=> datetime` reject invalid entries on Marshal and Unmarshal.
			if _, err := Marshal(RawAndTupleDT{Raw: badWire}); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Marshal RawTerm => datetime: expected ErrInvalidDateTime, got %v", err)
			}
			if _, err := Marshal(RawAndTupleDT{Tup: badTuple}); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Marshal Tuple => datetime: expected ErrInvalidDateTime, got %v", err)
			}
			mapWire, _ := Marshal(Map{{Key: Atom("raw"), Value: badTuple}})
			var holder RawAndTupleDT
			if err := Unmarshal(mapWire, &holder); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Unmarshal RawTerm => datetime: expected ErrInvalidDateTime, got %v", err)
			}
			mapWireTup, _ := Marshal(Map{{Key: Atom("tup"), Value: badTuple}})
			if err := Unmarshal(mapWireTup, &holder); !errors.Is(err, ErrInvalidDateTime) {
				t.Fatalf("Unmarshal Tuple => datetime: expected ErrInvalidDateTime, got %v", err)
			}
		})
	}

	// Structural and type wire errors for DateTime.
	hugeYear := new(big.Int).Lsh(big.NewInt(1), 100)
	wireErrorCases := []struct {
		name    string
		term    any
		wantErr error
	}{
		{"non_tuple_int", int64(42), ErrTypeMismatch},
		{"non_tuple_binary", Binary("2026-11-24"), ErrTypeMismatch},
		{"non_tuple_list", []int{2026, 11, 24}, ErrTypeMismatch},
		{"empty_tuple", Tuple{}, ErrInvalidDateTime},
		{"outer_arity_1", Tuple{Tuple{2026, 1, 1}}, ErrInvalidDateTime},
		{"outer_arity_3", Tuple{Tuple{2026, 1, 1}, Tuple{0, 0, 0}, Atom("utc")}, ErrInvalidDateTime},
		{"date_not_tuple", Tuple{int64(2026), Tuple{0, 0, 0}}, ErrInvalidDateTime},
		{"time_not_tuple", Tuple{Tuple{2026, 1, 1}, int64(0)}, ErrInvalidDateTime},
		{"date_arity_2", Tuple{Tuple{2026, 1}, Tuple{0, 0, 0}}, ErrInvalidDateTime},
		{"time_arity_2", Tuple{Tuple{2026, 1, 1}, Tuple{0, 0}}, ErrInvalidDateTime},
		{"date_arity_4", Tuple{Tuple{2026, 1, 1, 0}, Tuple{0, 0, 0}}, ErrInvalidDateTime},
		{"float_year", Tuple{Tuple{float64(2026.0), 1, 1}, Tuple{0, 0, 0}}, ErrInvalidDateTime},
		{"atom_month", Tuple{Tuple{2026, Atom("jan"), 1}, Tuple{0, 0, 0}}, ErrInvalidDateTime},
		{"binary_second", Tuple{Tuple{2026, 1, 1}, Tuple{0, 0, Binary("0")}}, ErrInvalidDateTime},
		{"huge_bignum_year", Tuple{Tuple{hugeYear, 1, 1}, Tuple{0, 0, 0}}, ErrInvalidDateTime},
	}

	for _, tc := range wireErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Marshal(tc.term)
			if err != nil {
				t.Fatalf("Marshal %s failed: %v", tc.name, err)
			}
			var dst DateTime
			if err := Unmarshal(raw, &dst); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Unmarshal %s: expected %v, got %v", tc.name, tc.wantErr, err)
			}
		})
	}

	// Trailing bytes, truncated bytes, and nil receiver.
	validRaw, _ := Marshal(DateTime{2026, 11, 24, 19, 50, 41})
	var dst DateTime
	if err := (&dst).UnmarshalETF(append(append([]byte(nil), validRaw...), TagNil)); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("expected ErrTrailingBytes, got %v", err)
	}
	if err := (&dst).UnmarshalETF(validRaw[:len(validRaw)-2]); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF, got %v", err)
	}
	var nilDT *DateTime
	if err := nilDT.UnmarshalETF(validRaw); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget for nil *DateTime, got %v", err)
	}
}
