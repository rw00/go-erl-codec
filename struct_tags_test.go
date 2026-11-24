package etf

import (
	"errors"
	"strings"
	"testing"
)

func TestStructTagValidation(t *testing.T) {
	type MissingTag struct {
		ID int
	}
	if _, err := Marshal(MissingTag{ID: 1}); err == nil {
		t.Fatal("expected InvalidTagError for missing etf tag")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	type MissingKeyType struct {
		ID int `etf:"id"`
	}
	if _, err := Marshal(MissingKeyType{ID: 1}); err == nil {
		t.Fatal("expected InvalidTagError for missing <name>:<key_type>")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	type EmptyKeyName struct {
		ID int `etf:":atom"`
	}
	if _, err := Marshal(EmptyKeyName{ID: 1}); err == nil {
		t.Fatal("expected InvalidTagError for empty key name")
	}

	type OmitemptyRejected struct {
		ID int `etf:"id:atom, omitempty"`
	}
	if _, err := Marshal(OmitemptyRejected{ID: 1}); err == nil {
		t.Fatal("expected InvalidTagError for removed omitempty option")
	}

	type DuplicateKey struct {
		A int `etf:"same:atom"`
		B int `etf:"same:atom"`
	}
	if _, err := Marshal(DuplicateKey{}); err == nil {
		t.Fatal("expected InvalidTagError for duplicate atom key")
	}

	type DuplicateBoolAtomKey struct {
		A int `etf:"true:atom"`
		B int `etf:"true:boolean"`
	}
	if _, err := Marshal(DuplicateBoolAtomKey{}); err == nil {
		t.Fatal("expected InvalidTagError for atom true colliding with boolean true")
	}

	type BadBoolKey struct {
		A int `etf:"maybe:boolean"`
	}
	if _, err := Marshal(BadBoolKey{}); err == nil {
		t.Fatal("expected InvalidTagError for non-boolean key name")
	}

	type BadIntKey struct {
		A int `etf:"abc:integer"`
	}
	if _, err := Marshal(BadIntKey{}); err == nil {
		t.Fatal("expected InvalidTagError for non-integer key name")
	}

	type BadNestedType struct {
		A []string `etf:"a:atom => list(unknown_type)"`
	}
	if _, err := Marshal(BadNestedType{}); err == nil {
		t.Fatal("expected InvalidTagError for invalid list element type")
	}

	type ParameterizedMap struct {
		M map[string]int `etf:"m:atom => map(binary=>integer)"`
	}
	if _, err := Marshal(ParameterizedMap{}); err == nil {
		t.Fatal("expected InvalidTagError for parameterized map(K=>V)")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	type DupRequired struct {
		A int `etf:"a:atom, required, required"`
	}
	if _, err := Marshal(DupRequired{}); err == nil {
		t.Fatal("expected InvalidTagError for duplicate required")
	}

	type DupNull struct {
		A *int `etf:"a:atom, null=nil, null=none"`
	}
	if _, err := Marshal(DupNull{}); err == nil {
		t.Fatal("expected InvalidTagError for duplicate null=")
	}

	type EmptyValueType struct {
		A int `etf:"a:atom => , required"`
	}
	if _, err := Marshal(EmptyValueType{}); err == nil {
		t.Fatal("expected InvalidTagError for empty value type after =>")
	}

	type KeyWithParens struct {
		Amount int `etf:"price (USD):binary => integer"`
	}
	if _, err := Marshal(KeyWithParens{}); err == nil {
		t.Fatal("expected InvalidTagError for key name with spaces/parentheses")
	}

	type KeyWithCommaQuoted struct {
		A int `etf:"'a,b':binary => integer"`
	}
	if _, err := Marshal(KeyWithCommaQuoted{}); err == nil {
		t.Fatal("expected InvalidTagError for quoted comma key name")
	}

	type KeyWithArrowQuoted struct {
		A int `etf:"'a => b':binary => integer"`
	}
	if _, err := Marshal(KeyWithArrowQuoted{}); err == nil {
		t.Fatal("expected InvalidTagError for quoted arrow key name")
	}

	type KeyQuotedEmpty struct {
		A int `etf:"'':binary => integer"`
	}
	if _, err := Marshal(KeyQuotedEmpty{}); err == nil {
		t.Fatal("expected InvalidTagError for quoted empty key name")
	}

	type KeyWithSpace struct {
		A int `etf:"order id:atom => integer"`
	}
	if _, err := Marshal(KeyWithSpace{}); err == nil {
		t.Fatal("expected InvalidTagError for key name with whitespace")
	}

	type KeyWithCloseParen struct {
		A int `etf:"amount):atom => integer"`
	}
	if _, err := Marshal(KeyWithCloseParen{}); err == nil {
		t.Fatal("expected InvalidTagError for key name with ')'")
	}

	type KeyWithOpenParen struct {
		A int `etf:"amount(:atom => integer"`
	}
	if _, err := Marshal(KeyWithOpenParen{}); err == nil {
		t.Fatal("expected InvalidTagError for key name with '('")
	}

	type KeyWithBackslash struct {
		A int `etf:"order\\id:binary => integer"`
	}
	if _, err := Marshal(KeyWithBackslash{}); err == nil {
		t.Fatal("expected InvalidTagError for key name with backslash")
	}
}

func TestMixedKeyStructAndRequiredFields(t *testing.T) {
	// Payment request with mixed atom/binary keys:
	// #{ user_id => ~"abc123", ~"user_id" => ~"abc123", amount => 41.5, currency => ~"EUR" }
	type PaymentRequest struct {
		UserID       string  `etf:"user_id:atom => binary, required"`
		LegacyUserID string  `etf:"user_id:binary => binary, required"`
		Amount       float64 `etf:"amount:atom => number, required"`
		Currency     string  `etf:"currency:atom, required"`
		InternalNote string  `etf:"-"`
	}

	in := PaymentRequest{
		UserID:       "abc123",
		LegacyUserID: "legacy-abc123",
		Amount:       41.5,
		Currency:     "EUR",
		InternalNote: "ignored",
	}
	raw, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal PaymentRequest failed: %v", err)
	}

	// Verify wire map has distinct Atom("user_id"), Binary("user_id"), Atom("amount"), Atom("currency").
	var m Map
	if err := Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal into Map failed: %v", err)
	}
	if len(m) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(m))
	}
	if v, ok := m.Get(Atom("user_id")); !ok || !Equal(v, Binary("abc123")) {
		t.Fatalf("missing Atom(user_id): %v %v", v, ok)
	}
	if v, ok := m.Get(Binary("user_id")); !ok || !Equal(v, Binary("legacy-abc123")) {
		t.Fatalf("missing Binary(user_id): %v %v", v, ok)
	}
	if v, ok := m.Get(Atom("amount")); !ok || !Equal(v, 41.5) {
		t.Fatalf("missing Atom(amount): %v %v", v, ok)
	}
	if v, ok := m.Get(Atom("currency")); !ok || !Equal(v, Binary("EUR")) {
		t.Fatalf("missing Atom(currency): %v %v", v, ok)
	}

	// Round-trip back into PaymentRequest.
	var out PaymentRequest
	if err := Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal PaymentRequest failed: %v", err)
	}
	if out.UserID != "abc123" || out.LegacyUserID != "legacy-abc123" || out.Amount != 41.5 || out.Currency != "EUR" || out.InternalNote != "" {
		t.Fatalf("unexpected decoded struct: %+v", out)
	}

	// Missing required field fails with ErrMissingRequiredField.
	partialRaw, _ := Marshal(Map{
		{Key: Atom("user_id"), Value: Binary("abc123")},
		{Key: Binary("user_id"), Value: Binary("legacy-abc123")},
		{Key: Atom("amount"), Value: 41.5},
	})
	var missing PaymentRequest
	err = Unmarshal(partialRaw, &missing)
	if !errors.Is(err, ErrMissingRequiredField) {
		t.Fatalf("expected ErrMissingRequiredField, got %v", err)
	}
}

func TestStructTagValidKeyNames(t *testing.T) {
	type ValidKeys struct {
		Namespaced string `etf:"user:id:binary => binary, required"`
		Hyphenated string `etf:"order-id:atom => binary, required"`
		Dotted     string `etf:"api.version:binary => binary, required"`
		Dash       int    `etf:"-:atom => integer, required"`
	}

	in := ValidKeys{
		Namespaced: "usr_123",
		Hyphenated: "ord_456",
		Dotted:     "v2.1",
		Dash:       1,
	}
	raw, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal ValidKeys failed: %v", err)
	}
	var m Map
	if err := Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal Map failed: %v", err)
	}
	for _, tc := range []struct {
		k any
		v any
	}{
		{Binary("user:id"), Binary("usr_123")},
		{Atom("order-id"), Binary("ord_456")},
		{Binary("api.version"), Binary("v2.1")},
		{Atom("-"), int64(1)},
	} {
		got, ok := m.Get(tc.k)
		if !ok || !Equal(got, tc.v) {
			t.Fatalf("missing key %#v: got=%#v ok=%v", tc.k, got, ok)
		}
	}

	var out ValidKeys
	if err := Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal ValidKeys failed: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: want=%+v got=%+v", in, out)
	}
}

func TestOptionalNilOmissionVsRequiredAndZeroValues(t *testing.T) {
	type S struct {
		// Non-required nilable fields: omitted when nil, encoded when non-nil (even empty).
		OptPtr   *int           `etf:"opt_ptr:atom => integer"`
		OptAny   any            `etf:"opt_any:atom"`
		OptSlice []int          `etf:"opt_slice:atom => list(integer)"`
		OptMap   map[string]int `etf:"opt_map:atom => map"`
		// Non-required non-nilable zero values: still encoded.
		ZeroInt  int    `etf:"zero_int:atom => integer"`
		ZeroBool bool   `etf:"zero_bool:atom => boolean"`
		ZeroStr  string `etf:"zero_str:atom => binary"`
		// Required nil pointer: still encoded as null atom.
		ReqPtr *int `etf:"req_ptr:atom => integer, required, null=nil"`
	}

	raw, err := Marshal(S{})
	if err != nil {
		t.Fatalf("Marshal S{} failed: %v", err)
	}
	var m Map
	if err := Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal Map failed: %v", err)
	}
	// Nil optional fields must be omitted.
	for _, omitted := range []Atom{"opt_ptr", "opt_any", "opt_slice", "opt_map"} {
		if _, ok := m.Get(omitted); ok {
			t.Fatalf("expected nil optional field %q to be omitted", omitted)
		}
	}
	// Non-nil zero values and required nil pointer must be present.
	for _, tc := range []struct {
		k Atom
		v any
	}{
		{"zero_int", int64(0)},
		{"zero_bool", false},
		{"zero_str", Binary("")},
		{"req_ptr", Atom("nil")},
	} {
		got, ok := m.Get(tc.k)
		if !ok || !Equal(got, tc.v) {
			t.Fatalf("field %q: want=%#v got=%#v ok=%v", tc.k, tc.v, got, ok)
		}
	}

	// Non-nil empty slice and non-nil empty map are encoded even when not required.
	rawEmpty, err := Marshal(S{OptSlice: []int{}, OptMap: map[string]int{}})
	if err != nil {
		t.Fatalf("Marshal non-nil empty slice/map failed: %v", err)
	}
	var m2 Map
	if err := Unmarshal(rawEmpty, &m2); err != nil {
		t.Fatalf("Unmarshal m2 failed: %v", err)
	}
	if _, ok := m2.Get(Atom("opt_slice")); !ok {
		t.Fatal("expected non-nil empty slice to be encoded")
	}
	if _, ok := m2.Get(Atom("opt_map")); !ok {
		t.Fatal("expected non-nil empty map to be encoded")
	}
}

type ExportedEmbedded struct {
	X int `etf:"x:atom => integer"`
}

type unexportedEmbedded struct {
	Y int `etf:"y:atom => integer"`
}

func TestEmbeddedPointerStructs(t *testing.T) {
	rawX, _ := Marshal(Map{{Key: Atom("x"), Value: 10}})
	rawY, _ := Marshal(Map{{Key: Atom("y"), Value: 20}})

	// Exported nil embedded pointer struct is allocated on decode.
	type WithExportedPtr struct {
		*ExportedEmbedded
	}
	var ep WithExportedPtr
	if err := Unmarshal(rawX, &ep); err != nil {
		t.Fatalf("Unmarshal into nil *ExportedEmbedded failed: %v", err)
	}
	if ep.ExportedEmbedded == nil || ep.X != 10 {
		t.Fatalf("unexpected ep: %+v", ep)
	}

	// Unexported embedded value struct works cleanly.
	type WithUnexportedVal struct {
		unexportedEmbedded
	}
	var uv WithUnexportedVal
	if err := Unmarshal(rawY, &uv); err != nil {
		t.Fatalf("Unmarshal into unexportedEmbedded value failed: %v", err)
	}
	if uv.Y != 20 {
		t.Fatalf("unexpected uv: %+v", uv)
	}

	// Unexported nil embedded pointer struct must return an error, NOT panic!
	type WithUnexportedPtr struct {
		*unexportedEmbedded
	}
	var up WithUnexportedPtr
	if err := Unmarshal(rawY, &up); err == nil {
		t.Fatalf("expected error decoding into nil *unexportedEmbedded, got nil")
	}

	// Pre-initialized non-nil *unexportedEmbedded decodes cleanly.
	upInit := WithUnexportedPtr{unexportedEmbedded: &unexportedEmbedded{}}
	if err := Unmarshal(rawY, &upInit); err != nil {
		t.Fatalf("Unmarshal into non-nil *unexportedEmbedded failed: %v", err)
	}
	if upInit.Y != 20 {
		t.Fatalf("unexpected upInit: %+v", upInit)
	}
}

func TestDisallowUnknownFieldsAndFloatKeyErrors(t *testing.T) {
	type S struct {
		A int `etf:"a:atom => integer"`
	}

	// Unknown atom key returns ErrUnknownField.
	raw, _ := Marshal(Map{
		{Key: Atom("a"), Value: 1},
		{Key: Atom("extra"), Value: 2},
	})
	var s S
	if err := Unmarshal(raw, &s, WithDisallowUnknownFields()); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField, got %v", err)
	}

	// Unknown float key includes formatted float value in error (not empty "").
	rawFloatUnknown, _ := Marshal(Map{{Key: float64(3.5), Value: 1}})
	err := Unmarshal(rawFloatUnknown, &s, WithDisallowUnknownFields())
	if !errors.Is(err, ErrUnknownField) || !strings.Contains(err.Error(), "3.5") {
		t.Fatalf("expected ErrUnknownField mentioning 3.5, got %v", err)
	}

	// Duplicate float key includes formatted float value in error (not empty "").
	rawFloatDup, _ := Marshal(Map{
		{Key: float64(1.25), Value: 1},
		{Key: float64(1.25), Value: 2},
	})
	err = Unmarshal(rawFloatDup, &s)
	if !errors.Is(err, ErrDuplicateMapKey) || !strings.Contains(err.Error(), "1.25") {
		t.Fatalf("expected ErrDuplicateMapKey mentioning 1.25, got %v", err)
	}
}

func TestEmbeddedStructDiamondShadowingAndCycles(t *testing.T) {
	type Base struct {
		ID int `etf:"id:atom => integer"`
	}
	type Left struct {
		Base
	}
	type Right struct {
		Base
	}
	type RightPtr struct {
		*Base
	}

	// Diamond embedding at same depth must return *InvalidTagError.
	type Diamond struct {
		Left
		Right
	}
	if _, err := Marshal(Diamond{}); err == nil {
		t.Fatal("expected *InvalidTagError for diamond embedding of Base at same depth")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	// Mixed value + pointer diamond embedding at same depth must return *InvalidTagError.
	type MixedDiamond struct {
		Left
		RightPtr
	}
	if _, err := Marshal(MixedDiamond{}); err == nil {
		t.Fatal("expected *InvalidTagError for mixed value/pointer diamond embedding")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	// Deep diamond (depth 3) must return *InvalidTagError.
	type DeepLeft struct {
		Left
	}
	type DeepRight struct {
		Right
	}
	type DeepDiamond struct {
		DeepLeft
		DeepRight
	}
	if _, err := Marshal(DeepDiamond{}); err == nil {
		t.Fatal("expected *InvalidTagError for deep diamond embedding")
	} else if _, ok := err.(*InvalidTagError); !ok {
		t.Fatalf("expected *InvalidTagError, got %T: %v", err, err)
	}

	// Shallower field shadows diamond duplicate at deeper level.
	type ShadowedDiamond struct {
		Left
		Right
		ID int `etf:"id:atom => integer"`
	}
	raw, err := Marshal(ShadowedDiamond{ID: 99, Left: Left{Base: Base{ID: 1}}, Right: Right{Base: Base{ID: 2}}})
	if err != nil {
		t.Fatalf("Marshal ShadowedDiamond failed: %v", err)
	}
	var sd ShadowedDiamond
	if err := Unmarshal(raw, &sd); err != nil {
		t.Fatalf("Unmarshal ShadowedDiamond failed: %v", err)
	}
	if sd.ID != 99 {
		t.Fatalf("expected shadowed ID=99, got %d", sd.ID)
	}

	// Same Base embedded at different depths: shallower wins regardless of field order.
	type OuterShallowSecond struct {
		Left // Base at depth 2
		Base // Base at depth 1
	}
	raw2, err := Marshal(OuterShallowSecond{Left: Left{Base: Base{ID: 1}}, Base: Base{ID: 42}})
	if err != nil {
		t.Fatalf("Marshal OuterShallowSecond failed: %v", err)
	}
	var oss OuterShallowSecond
	if err := Unmarshal(raw2, &oss); err != nil || oss.ID != 42 {
		t.Fatalf("expected shallower Base.ID=42, got %+v err=%v", oss, err)
	}

	// Recursive embedded pointer struct terminates cleanly without infinite loop.
	type Cyclic struct {
		*Cyclic
		ID int `etf:"id:atom => integer"`
	}
	rawCyc, err := Marshal(Cyclic{ID: 7})
	if err != nil {
		t.Fatalf("Marshal Cyclic failed: %v", err)
	}
	var cyc Cyclic
	if err := Unmarshal(rawCyc, &cyc); err != nil || cyc.ID != 7 {
		t.Fatalf("expected Cyclic ID=7, got %+v err=%v", cyc, err)
	}
}
