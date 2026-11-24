package etf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

type customCSV []string

func (c customCSV) MarshalETF() ([]byte, error) {
	return Marshal(Binary(strings.Join(c, ",")))
}

func (c *customCSV) UnmarshalETF(data []byte) error {
	var s string
	if err := Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*c = nil
		return nil
	}
	*c = strings.Split(s, ",")
	return nil
}

type customStruct struct {
	Code int
	Tag  string
}

func (c customStruct) MarshalETF() ([]byte, error) {
	return Marshal(Map{
		{Key: Atom("c"), Value: c.Code},
		{Key: Atom("t"), Value: Atom(c.Tag)},
	})
}

func (c *customStruct) UnmarshalETF(data []byte) error {
	var m Map
	if err := Unmarshal(data, &m); err != nil {
		return err
	}
	if v, ok := m.Get(Atom("c")); ok {
		if i, ok := v.(int64); ok {
			c.Code = int(i)
		}
	}
	if v, ok := m.Get(Atom("t")); ok {
		if a, ok := v.(Atom); ok {
			c.Tag = string(a)
		}
	}
	return nil
}

func TestNestedStructsListsMapsAndCustomMarshalers(t *testing.T) {
	type OrderMeta struct {
		SchemaVersion int `etf:"schema_version:atom => integer, required"`
	}
	type ShippingAddress struct {
		City       string `etf:"city:atom => binary, required"`
		PostalCode string `etf:"postal_code:atom => binary"`
	}
	type OrderRequest struct {
		OrderMeta
		UserID       string            `etf:"user_id:atom => binary, required"`
		Capture      bool              `etf:"capture:atom => boolean, required"`
		Amount       float64           `etf:"amount:atom => number"`
		Tags         []string          `etf:"tags:atom => list(atom)"`
		Installments [][]int           `etf:"installments:atom => list(list(integer))"`
		Metadata     map[string][]Atom `etf:"metadata:atom => map"`
		Shipping     ShippingAddress   `etf:"shipping:atom, required"`
		Billing      *ShippingAddress  `etf:"billing:atom"`
		RetryCount   *int              `etf:"retry_count:atom => integer, required"`
		LegacyRef    string            `etf:"legacy_ref:atom => charlist"`
		SKUs         customCSV         `etf:"skus:atom"`
		Receipt      customStruct      `etf:"receipt:atom"`
		GatewayRaw   RawTerm           `etf:"gateway_raw:atom"`
		TierLabel    string            `etf:"42:integer => binary"`
		FeeRate      string            `etf:"1.5:float => atom"`
		Priority     int               `etf:"true:boolean => integer"`
		CouponCode   *string           `etf:"coupon_code:atom => binary"`
	}

	rawTuple := RawTerm{VersionMagic, TagSmallTuple, 2, TagSmallAtomUTF8, 2, 'o', 'k', TagSmallInt, 7}
	in := OrderRequest{
		OrderMeta:    OrderMeta{SchemaVersion: 2},
		UserID:       "abc123",
		Capture:      true,
		Amount:       41.5,
		Tags:         []string{"vip", "recurring"},
		Installments: [][]int{{2000, 1}, {2150, 2}},
		Metadata:     map[string][]Atom{"region": {"eu", "de"}},
		Shipping:     ShippingAddress{City: "Berlin", PostalCode: "10115"},
		Billing:      nil, // omitted on encode -> decodes back as nil
		RetryCount:   nil, // required pointer with undefined -> allowed on decode & encode
		LegacyRef:    "ref-héllo",
		SKUs:         customCSV{"sku_1", "sku_2", "sku_3"},
		Receipt:      customStruct{Code: 200, Tag: "approved"},
		GatewayRaw:   rawTuple,
		TierLabel:    "enterprise",
		FeeRate:      "standard_fee",
		Priority:     99,
	}

	raw, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal OrderRequest failed: %v", err)
	}

	var out OrderRequest
	if err := Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal OrderRequest failed: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\nin =%+v\nout=%+v", in, out)
	}

	// Verify `boolean` tag rejects non-boolean atom like `maybe`.
	badBoolRaw, _ := Marshal(Map{
		{Key: Atom("capture"), Value: Atom("maybe")},
	})
	type OnlyCapture struct {
		Capture bool `etf:"capture:atom => boolean, required"`
	}
	var oc OnlyCapture
	if err := Unmarshal(badBoolRaw, &oc); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for non-boolean atom, got %v", err)
	}

	// Verify `list(atom)` rejects list containing a binary element.
	badListRaw, _ := Marshal(Map{
		{Key: Atom("tags"), Value: []any{Atom("vip"), Binary("not_atom")}},
	})
	type OnlyTags struct {
		Tags []string `etf:"tags:atom => list(atom)"`
	}
	var ot OnlyTags
	if err := Unmarshal(badListRaw, &ot); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for binary inside list(atom), got %v", err)
	}
}

func TestArbitraryMapKeysAndNonComparableProtection(t *testing.T) {
	// Map with atom, binary, int, float, list, and nested map keys!
	m := Map{
		{Key: Atom("user_id"), Value: Binary("abc123")},
		{Key: Binary("user_id"), Value: Binary("legacy-abc123")},
		{Key: int64(1), Value: Atom("tier_one")},
		{Key: float64(1.0), Value: Atom("rate_one")},
		{Key: []any{int64(1), int64(2)}, Value: Atom("bucket_key")},
		{Key: Map{{Key: Atom("currency"), Value: Binary("EUR")}}, Value: Binary("eu_ledger")},
	}

	raw, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal arbitrary Map failed: %v", err)
	}

	var decoded any
	if err := Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal arbitrary Map into any failed: %v", err)
	}
	if !Equal(m, decoded) {
		t.Fatalf("arbitrary Map round-trip mismatch:\nwant=%#v\ngot =%#v", m, decoded)
	}

	// Decoding non-comparable keys into map[any]any must return ErrUncomparableMapKey (never panic!).
	var goMap map[any]any
	if err := Unmarshal(raw, &goMap); !errors.Is(err, ErrUncomparableMapKey) {
		t.Fatalf("expected ErrUncomparableMapKey, got %v", err)
	}

	// Duplicate map key rejection.
	dupRaw := []byte{
		VersionMagic, TagMap, 0, 0, 0, 2,
		TagSmallInt, 1, TagSmallInt, 10,
		TagInt, 0, 0, 0, 1, TagSmallInt, 20, // same integer 1 via INTEGER_EXT!
	}
	var outMap Map
	if err := Unmarshal(dupRaw, &outMap); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey, got %v", err)
	}
}

func TestBitstringAndImproperList(t *testing.T) {
	bs := Bitstring{Data: []byte{0xFF, 0xF0}, Bits: 3}
	raw, err := Marshal(bs)
	if err != nil {
		t.Fatalf("Marshal Bitstring failed: %v", err)
	}
	var gotBS Bitstring
	if err := Unmarshal(raw, &gotBS); err != nil {
		t.Fatalf("Unmarshal Bitstring failed: %v", err)
	}
	// Last byte masked to top 3 bits: 0xF0 & 0xE0 = 0xE0.
	if gotBS.Bits != 3 || !reflect.DeepEqual(gotBS.Data, []byte{0xFF, 0xE0}) {
		t.Fatalf("unexpected bitstring: %+v", gotBS)
	}

	// Improper list [1, 2 | ok].
	il := ImproperList{Elements: []any{int64(1), int64(2)}, Tail: Atom("ok")}
	rawIL, err := Marshal(il)
	if err != nil {
		t.Fatalf("Marshal ImproperList failed: %v", err)
	}
	var slice []int
	if err := Unmarshal(rawIL, &slice); !errors.Is(err, ErrImproperList) {
		t.Fatalf("expected ErrImproperList decoding into slice, got %v", err)
	}
	var gotAny any
	if err := Unmarshal(rawIL, &gotAny); err != nil {
		t.Fatalf("Unmarshal ImproperList into any failed: %v", err)
	}
	if !Equal(il, gotAny) {
		t.Fatalf("ImproperList mismatch: want=%#v got=%#v", il, gotAny)
	}

	// Equal symmetry & trailing-bit masking for Bitstring.
	b8 := Bitstring{Data: []byte("hi"), Bits: 8}
	if !Equal(b8, Binary("hi")) || !Equal(Binary("hi"), b8) {
		t.Fatalf("Equal asymmetry between Bitstring(bits=8) and Binary")
	}
	if !Equal(b8, []byte("hi")) || !Equal([]byte("hi"), b8) {
		t.Fatalf("Equal asymmetry between Bitstring(bits=8) and []byte")
	}
	if !Equal(b8, "hi") || !Equal("hi", b8) {
		t.Fatalf("Equal asymmetry between Bitstring(bits=8) and string")
	}
	emptyBS := Bitstring{Data: nil, Bits: 0}
	if !Equal(emptyBS, Binary("")) || !Equal(Binary(""), emptyBS) {
		t.Fatalf("Equal asymmetry between empty Bitstring and empty Binary")
	}
	if !Equal(Bitstring{Data: []byte{0xFF}, Bits: 3}, Bitstring{Data: []byte{0xE0}, Bits: 3}) {
		t.Fatalf("Equal should mask unused trailing bits in Bitstring")
	}
	if Equal(Bitstring{Data: []byte{0xFF}, Bits: 3}, Bitstring{Data: []byte{0xC0}, Bits: 3}) {
		t.Fatalf("Equal should distinguish significant bits in Bitstring")
	}

	// Invalid Bitstring Bits values on Marshal and Unmarshal.
	for _, invalid := range []Bitstring{
		{Data: []byte{1}, Bits: 0},
		{Data: []byte{1}, Bits: 9},
		{Data: nil, Bits: 1},
	} {
		if _, err := Marshal(invalid); !errors.Is(err, ErrInvalidBitstring) {
			t.Fatalf("expected ErrInvalidBitstring for %+v, got %v", invalid, err)
		}
	}
	for _, rawBad := range [][]byte{
		{VersionMagic, TagBitBinary, 0, 0, 0, 0, 0},    // len=0
		{VersionMagic, TagBitBinary, 0, 0, 0, 1, 0, 1}, // bits=0
		{VersionMagic, TagBitBinary, 0, 0, 0, 1, 9, 1}, // bits=9
	} {
		var dst Bitstring
		if err := Unmarshal(rawBad, &dst); !errors.Is(err, ErrInvalidBitstring) {
			t.Fatalf("expected ErrInvalidBitstring decoding %v, got %v", rawBad, err)
		}
	}
}

func TestGoMapDuplicateKeysAndAllowDuplicateOption(t *testing.T) {
	// Pre-populated Go map must not falsely return ErrDuplicateMapKey.
	raw, _ := Marshal(Map{{Key: Binary("a"), Value: 2}})
	prepop := map[string]int{"a": 1, "keep": 9}
	if err := Unmarshal(raw, &prepop); err != nil {
		t.Fatalf("Unmarshal into pre-populated map failed: %v", err)
	}
	if prepop["a"] != 2 || prepop["keep"] != 9 {
		t.Fatalf("unexpected map contents: %v", prepop)
	}

	// Duplicate *big.Int keys in map[any]any must return ErrDuplicateMapKey.
	big1, _ := new(big.Int).SetString("1000000000000000000000000000000", 10)
	big2, _ := new(big.Int).SetString("1000000000000000000000000000000", 10)
	dupBigRaw, _ := Marshal(Map{
		{Key: big1, Value: 1},
		{Key: big2, Value: 2},
	})
	var anyMap map[any]any
	if err := Unmarshal(dupBigRaw, &anyMap); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for duplicate *big.Int keys, got %v", err)
	}

	// OTP 27: +0.0 and -0.0 are distinct float keys and must not trigger ErrDuplicateMapKey.
	zeroRaw, _ := Marshal(Map{
		{Key: float64(0.0), Value: 1},
		{Key: math.Copysign(0, -1), Value: 2},
	})
	var zeroMap map[any]any
	if err := Unmarshal(zeroRaw, &zeroMap); err != nil {
		t.Fatalf("expected +0.0 and -0.0 not to collide as duplicate keys, got %v", err)
	}

	// WithAllowDuplicateMapKeys allows duplicate keys (last value wins).
	dupRaw, _ := Marshal(Map{
		{Key: Atom("k"), Value: 1},
		{Key: Atom("k"), Value: 2},
	})
	var allowedGo map[string]int
	if err := Unmarshal(dupRaw, &allowedGo, WithAllowDuplicateMapKeys()); err != nil {
		t.Fatalf("WithAllowDuplicateMapKeys failed on Go map: %v", err)
	}
	if allowedGo["k"] != 2 {
		t.Fatalf("expected last value 2, got %d", allowedGo["k"])
	}
	type S struct {
		K int `etf:"k:atom => integer"`
	}
	var allowedStruct S
	if err := Unmarshal(dupRaw, &allowedStruct, WithAllowDuplicateMapKeys()); err != nil || allowedStruct.K != 2 {
		t.Fatalf("WithAllowDuplicateMapKeys failed on struct: %+v err=%v", allowedStruct, err)
	}
}

func TestNilAtomOptionsNullTagAndRawTermPtr(t *testing.T) {
	// Elixir-style WithNilAtom / WithDecodeNilAtom.
	var nilPtr *int
	rawNil, err := Marshal(nilPtr, WithNilAtom(Atom("nil")))
	if err != nil {
		t.Fatal(err)
	}
	var gotAtom Atom
	if err := Unmarshal(rawNil, &gotAtom); err != nil || gotAtom != Atom("nil") {
		t.Fatalf("expected Atom(nil), got %q err=%v", gotAtom, err)
	}
	five := 5
	dstPtr := &five
	if err := Unmarshal(rawNil, &dstPtr, WithDecodeNilAtom(Atom("nil"))); err != nil {
		t.Fatalf("WithDecodeNilAtom failed: %v", err)
	}
	if dstPtr != nil {
		t.Fatalf("expected nil pointer, got %v", *dstPtr)
	}

	// Per-field null=<atom> tag and *RawTerm nil pointer round-trip.
	type S struct {
		ReqOpt *int     `etf:"opt:atom => integer, required, null=none"`
		Raw    RawTerm  `etf:"raw:atom"`
		OptRaw *RawTerm `etf:"opt_raw:atom, required"`
	}
	undefRaw, _ := Marshal(Atom("undefined"))
	in := S{ReqOpt: nil, Raw: undefRaw, OptRaw: nil}
	encoded, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal S failed: %v", err)
	}
	var m Map
	if err := Unmarshal(encoded, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m.Get(Atom("opt")); !ok || !Equal(v, Atom("none")) {
		t.Fatalf("expected opt=none, got %#v ok=%v", v, ok)
	}

	var out S
	if err := Unmarshal(encoded, &out); err != nil {
		t.Fatalf("Unmarshal S failed: %v", err)
	}
	if out.ReqOpt != nil || out.OptRaw != nil || !Equal(out.Raw, RawTerm(undefRaw)) {
		t.Fatalf("unexpected decoded S: %+v", out)
	}

	// Top-level Unmarshal into &RawTerm still captures undefined verbatim.
	var topRaw RawTerm
	if err := Unmarshal(undefRaw, &topRaw); err != nil {
		t.Fatalf("Unmarshal undefined into &RawTerm failed: %v", err)
	}
	if !Equal(topRaw, RawTerm(undefRaw)) {
		t.Fatalf("expected verbatim undefined RawTerm, got %v", topRaw)
	}
}

func TestPreallocationAmplificationGuard(t *testing.T) {
	// 5000 TagNil elements claiming to be a list of large structs: must fail on element 0
	// without allocating 5000 * 64 KiB (~320 MiB).
	type LargeElem struct {
		Buf [1 << 16]byte
		V   int `etf:"v:atom => integer"`
	}
	payload := make([]byte, 0, 1+1+4+5001)
	payload = append(payload, VersionMagic, TagList, 0, 0, 0x13, 0x88) // n = 5000
	for i := 0; i < 5001; i++ {
		payload = append(payload, TagNil)
	}
	var out []LargeElem
	if err := Unmarshal(payload, &out); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch, got %v", err)
	}
}

func TestGoEnumConstants(t *testing.T) {
	type OrderStatus int
	const (
		OrderPending OrderStatus = iota
		OrderPaid
		OrderRefunded
	)

	type RiskLevel uint8
	const (
		RiskLow  RiskLevel = 1
		RiskHigh RiskLevel = 2
	)

	type PaymentMethod string
	const (
		PaymentCard PaymentMethod = "card"
		PaymentSEPA PaymentMethod = "sepa"
	)

	// Type alias to etf.Atom encodes/decodes as an ETF atom even without a struct tag.
	type Currency = Atom
	const (
		CurrencyEUR Currency = "EUR"
		CurrencyUSD Currency = "USD"
	)

	type AutoCapture bool
	const AutoCaptureEnabled AutoCapture = true

	type PaymentOrder struct {
		Status      OrderStatus              `etf:"status:atom => integer, required"`
		Risk        RiskLevel                `etf:"risk_level:atom => integer, required"`
		Method      PaymentMethod            `etf:"method:atom => atom, required"`
		MethodBin   PaymentMethod            `etf:"method_bin:atom => binary, required"`
		Allowed     []PaymentMethod          `etf:"allowed_methods:atom => list(atom)"`
		RiskHistory []RiskLevel              `etf:"risk_history:atom => list(integer)"`
		ByCurrency  map[Currency]OrderStatus `etf:"by_currency:atom => map"`
		Currency    Currency                 `etf:"currency:atom, required"`
		Capture     AutoCapture              `etf:"auto_capture:atom => boolean, required"`
		OptStatus   *OrderStatus             `etf:"opt_status:atom => integer"`
	}

	in := PaymentOrder{
		Status:      OrderPaid,
		Risk:        RiskHigh,
		Method:      PaymentCard,
		MethodBin:   PaymentSEPA,
		Allowed:     []PaymentMethod{PaymentCard, PaymentSEPA},
		RiskHistory: []RiskLevel{RiskLow, RiskHigh},
		ByCurrency:  map[Currency]OrderStatus{CurrencyEUR: OrderPaid, CurrencyUSD: OrderPending},
		Currency:    CurrencyEUR,
		Capture:     AutoCaptureEnabled,
		OptStatus:   nil,
	}

	raw, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal enum PaymentOrder failed: %v", err)
	}

	// Verify exact ETF wire types via etf.Map.
	var wire Map
	if err := Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Unmarshal wire Map failed: %v", err)
	}
	for _, tc := range []struct {
		k    Atom
		want any
	}{
		{"status", int64(1)},
		{"risk_level", int64(2)},
		{"method", Atom("card")},
		{"method_bin", Binary("sepa")},
		{"allowed_methods", []any{Atom("card"), Atom("sepa")}},
		{"risk_history", []any{int64(1), int64(2)}},
		{"by_currency", Map{{Key: Atom("EUR"), Value: int64(1)}, {Key: Atom("USD"), Value: int64(0)}}},
		{"currency", Atom("EUR")},
		{"auto_capture", true},
	} {
		got, ok := wire.Get(tc.k)
		if !ok || !Equal(got, tc.want) {
			t.Fatalf("wire field %q: want=%#v got=%#v ok=%v", tc.k, tc.want, got, ok)
		}
	}
	if _, ok := wire.Get(Atom("opt_status")); ok {
		t.Fatal("expected nil optional opt_status to be omitted from wire map")
	}

	// Round-trip back into PaymentOrder (including under WithStrictTypes).
	var out PaymentOrder
	if err := Unmarshal(raw, &out, WithStrictTypes()); err != nil {
		t.Fatalf("Unmarshal enum PaymentOrder failed: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("enum round-trip mismatch:\nin =%+v\nout=%+v", in, out)
	}

	// Standalone enum values.
	rawStatus, err := Marshal(OrderRefunded)
	if err != nil {
		t.Fatal(err)
	}
	var gotStatus OrderStatus
	if err := Unmarshal(rawStatus, &gotStatus, WithStrictTypes()); err != nil || gotStatus != OrderRefunded {
		t.Fatalf("standalone OrderStatus round-trip failed: got=%v err=%v", gotStatus, err)
	}

	rawCurrency, err := Marshal(CurrencyUSD)
	if err != nil {
		t.Fatal(err)
	}
	var gotCurrency Currency
	if err := Unmarshal(rawCurrency, &gotCurrency, WithStrictTypes()); err != nil || gotCurrency != CurrencyUSD {
		t.Fatalf("standalone Currency alias round-trip failed: got=%v err=%v", gotCurrency, err)
	}

	// Overflow check still applies to narrow integer enum types (uint8).
	rawOverflow, _ := Marshal(300)
	var gotRisk RiskLevel
	if err := Unmarshal(rawOverflow, &gotRisk); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow decoding 300 into RiskLevel(uint8), got %v", err)
	}
}

func TestValBinaryByteAlignedBitstringAndLazyErrorPaths(t *testing.T) {
	// valBinary accepts byte-aligned BIT_BINARY_EXT (bits=8) and rejects non-byte-aligned (bits=3).
	type BinStruct struct {
		S  string    `etf:"s:atom => binary"`
		B  Binary    `etf:"b:atom => binary"`
		BS Bitstring `etf:"bs:atom => binary"`
		A  any       `etf:"a:atom => binary"`
	}
	bit8 := []byte{TagBitBinary, 0, 0, 0, 2, 8, 'o', 'k'}
	bit3 := []byte{TagBitBinary, 0, 0, 0, 1, 3, 0xE0}

	makeMap := func(k string, val []byte) []byte {
		out := []byte{VersionMagic, TagMap, 0, 0, 0, 1, TagSmallAtomUTF8, byte(len(k))}
		out = append(out, k...)
		out = append(out, val...)
		return out
	}

	var dst BinStruct
	if err := Unmarshal(makeMap("s", bit8), &dst); err != nil || dst.S != "ok" {
		t.Fatalf("valBinary string bits=8 failed: %q err=%v", dst.S, err)
	}
	if err := Unmarshal(makeMap("b", bit8), &dst); err != nil || dst.B != Binary("ok") {
		t.Fatalf("valBinary Binary bits=8 failed: %q err=%v", dst.B, err)
	}
	if err := Unmarshal(makeMap("bs", bit8), &dst); err != nil || dst.BS.Bits != 8 || string(dst.BS.Data) != "ok" {
		t.Fatalf("valBinary Bitstring bits=8 failed: %+v err=%v", dst.BS, err)
	}
	if err := Unmarshal(makeMap("a", bit8), &dst); err != nil || dst.A != Binary("ok") {
		t.Fatalf("valBinary any bits=8 failed: %#v err=%v", dst.A, err)
	}

	for _, k := range []string{"s", "b", "bs", "a"} {
		if err := Unmarshal(makeMap(k, bit3), &dst); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("valBinary %s bits=3: expected ErrTypeMismatch, got %v", k, err)
		}
	}

	// Lazy error path construction produces exact paths on nested errors.
	type Inner struct {
		Nums []int `etf:"nums:atom => list(integer), required"`
	}
	type Outer struct {
		Items map[string]Inner `etf:"items:atom => map, required"`
	}
	badNested, _ := Marshal(Map{
		{Key: Atom("items"), Value: Map{
			{Key: Binary("first"), Value: Map{
				{Key: Atom("nums"), Value: []any{int64(1), Atom("oops")}},
			}},
		}},
	})
	var o Outer
	err := Unmarshal(badNested, &o)
	var de *DecodeError
	if !errors.As(err, &de) || de.Path != "Items[first].Nums[1]" {
		t.Fatalf("expected DecodeError path Items[first].Nums[1], got %v", err)
	}

	// Struct without KeyCharlist fields still rejects duplicate STRING_EXT / LIST_EXT keys.
	type NoCharlist struct {
		A int `etf:"a:atom => integer"`
	}
	dupListKeys := []byte{
		VersionMagic, TagMap, 0, 0, 0, 2,
		TagString, 0, 2, 'h', 'i', TagSmallInt, 1,
		TagList, 0, 0, 0, 2, TagSmallInt, 'h', TagSmallInt, 'i', TagNil, TagSmallInt, 2,
	}
	var nc NoCharlist
	if err := Unmarshal(dupListKeys, &nc); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for STRING_EXT + LIST_EXT duplicate keys on struct, got %v", err)
	}
}

func TestEagerNilAtomValidationAndEqualFastPath(t *testing.T) {
	badUTF8 := Atom("\xff\xfe")
	tooLong := Atom(strings.Repeat("a", 256))

	// WithNilAtom validates eagerly even when encoding non-nil value.
	if _, err := Marshal(42, WithNilAtom(badUTF8)); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for WithNilAtom, got %v", err)
	}
	if _, err := Marshal(42, WithNilAtom(tooLong)); !errors.Is(err, ErrAtomTooLong) {
		t.Fatalf("expected ErrAtomTooLong for WithNilAtom, got %v", err)
	}

	// WithDecodeNilAtom validates eagerly even when decoding non-nil value.
	raw42, _ := Marshal(42)
	var dst int
	if err := Unmarshal(raw42, &dst, WithDecodeNilAtom(badUTF8)); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for WithDecodeNilAtom, got %v", err)
	}
	if err := Unmarshal(raw42, &dst, WithDecodeNilAtom(tooLong)); !errors.Is(err, ErrAtomTooLong) {
		t.Fatalf("expected ErrAtomTooLong for WithDecodeNilAtom, got %v", err)
	}

	// Equal fast-path integer comparisons across int64, uint64, *big.Int, big.Int.
	maxU64Big := new(big.Int).SetUint64(math.MaxUint64)
	minI64Big := big.NewInt(math.MinInt64)
	if !Equal(int64(math.MinInt64), minI64Big) || !Equal(minI64Big, int64(math.MinInt64)) {
		t.Fatal("Equal failed for MinInt64 vs *big.Int")
	}
	if !Equal(uint64(math.MaxUint64), maxU64Big) || !Equal(*maxU64Big, uint64(math.MaxUint64)) {
		t.Fatal("Equal failed for MaxUint64 vs *big.Int/big.Int")
	}
	if Equal(int64(-1), uint64(math.MaxUint64)) {
		t.Fatal("Equal falsely matched -1 and MaxUint64")
	}
	if !Equal(uint64(42), int8(42)) || !Equal(Charlist("AB"), []any{int64(65), uint8(66)}) {
		t.Fatal("Equal failed for small integer / charlist comparisons")
	}
}

func TestNumberFloatWholeEncodesAsInteger(t *testing.T) {
	type Request struct {
		Amount float64 `etf:"amount:atom => number"`
	}

	// 42.0 -> encoded as ETF integer (int64(42)).
	rawInt, err := Marshal(Request{Amount: 42.0})
	if err != nil {
		t.Fatalf("Marshal 42.0 failed: %v", err)
	}
	var m Map
	if err := Unmarshal(rawInt, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m.Get(Atom("amount"))
	if !ok {
		t.Fatal("missing amount")
	}
	if _, isInt := v.(int64); !isInt || v != int64(42) {
		t.Fatalf("expected int64(42) for 42.0 => number, got %T(%v)", v, v)
	}

	// 42.5 -> encoded as ETF float (float64(42.5)).
	rawFloat, err := Marshal(Request{Amount: 42.5})
	if err != nil {
		t.Fatalf("Marshal 42.5 failed: %v", err)
	}
	m = nil
	if err := Unmarshal(rawFloat, &m); err != nil {
		t.Fatal(err)
	}
	v, ok = m.Get(Atom("amount"))
	if !ok {
		t.Fatal("missing amount")
	}
	if _, isFloat := v.(float64); !isFloat || v != 42.5 {
		t.Fatalf("expected float64(42.5) for 42.5 => number, got %T(%v)", v, v)
	}

	// Large whole float (1e20) -> encoded as ETF *big.Int.
	rawBig, err := Marshal(Request{Amount: 1e20})
	if err != nil {
		t.Fatalf("Marshal 1e20 failed: %v", err)
	}
	m = nil
	if err := Unmarshal(rawBig, &m); err != nil {
		t.Fatal(err)
	}
	v, _ = m.Get(Atom("amount"))
	if _, isBig := v.(*big.Int); !isBig {
		t.Fatalf("expected *big.Int for 1e20 => number, got %T(%v)", v, v)
	}

	// NaN/Inf still rejected.
	if _, err := Marshal(Request{Amount: math.NaN()}); !errors.Is(err, ErrNonFiniteFloat) {
		t.Fatalf("expected ErrNonFiniteFloat for NaN, got %v", err)
	}

	// Fractional float64 with `=> integer` fails with ErrLossyCoercion.
	type IntOnly struct {
		AsFloat float64 `etf:"e:atom => integer, required"`
	}
	if _, err := Marshal(IntOnly{AsFloat: 42.5}); !errors.Is(err, ErrLossyCoercion) {
		t.Fatalf("expected ErrLossyCoercion for 42.5 => integer, got %v", err)
	}
}

func TestPassthroughAnyAndRawTermWithValueSpec(t *testing.T) {
	innerMap := Map{
		{Key: Atom("ip"), Value: Binary("127.0.0.1")},
		{Key: Binary("retries"), Value: int64(3)},
		{Key: int64(1), Value: []any{Atom("a"), float64(2.5)}},
	}
	outerRaw, err := Marshal(Map{
		{Key: Atom("request_info"), Value: innerMap},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. RawTerm with `=> map` preserves exact ETF map bytes across decode/encode and enforces map type.
	type WithRaw struct {
		RequestInfo RawTerm `etf:"request_info:atom => map"`
	}
	var wr WithRaw
	if err := Unmarshal(outerRaw, &wr); err != nil {
		t.Fatalf("Unmarshal WithRaw failed: %v", err)
	}
	reEncodedRaw, err := Marshal(wr)
	if err != nil {
		t.Fatalf("Marshal WithRaw failed: %v", err)
	}
	var decoded1, decoded2 any
	if err := Unmarshal(outerRaw, &decoded1); err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(reEncodedRaw, &decoded2); err != nil {
		t.Fatal(err)
	}
	if !Equal(decoded1, decoded2) {
		t.Fatalf("WithRaw round-trip mismatch:\nwant=%#v\ngot =%#v", decoded1, decoded2)
	}

	// Non-map wire term rejected on decode into RawTerm with `=> map`.
	notMapRaw, _ := Marshal(Map{{Key: Atom("request_info"), Value: int64(42)}})
	var wrBad WithRaw
	if err := Unmarshal(notMapRaw, &wrBad); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch decoding int into RawTerm => map, got %v", err)
	}
	// Non-map RawTerm rejected on encode with `=> map`.
	intRaw, _ := Marshal(int64(42))
	if _, err := Marshal(WithRaw{RequestInfo: RawTerm(intRaw)}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch encoding int RawTerm as => map, got %v", err)
	}

	// 2. `any` with `=> map` preserves arbitrary Erlang map across decode/encode and enforces map type.
	type WithAny struct {
		RequestInfo any `etf:"request_info:atom => map"`
	}
	var wa WithAny
	if err := Unmarshal(outerRaw, &wa); err != nil {
		t.Fatalf("Unmarshal WithAny failed: %v", err)
	}
	reEncodedAny, err := Marshal(wa)
	if err != nil {
		t.Fatalf("Marshal WithAny failed: %v", err)
	}
	var decoded3 any
	if err := Unmarshal(reEncodedAny, &decoded3); err != nil {
		t.Fatal(err)
	}
	if !Equal(decoded1, decoded3) {
		t.Fatalf("WithAny round-trip mismatch:\nwant=%#v\ngot =%#v", decoded1, decoded3)
	}
	var waBad WithAny
	if err := Unmarshal(notMapRaw, &waBad); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch decoding int into any => map, got %v", err)
	}
}

func TestRawTermDeepSpecValidation(t *testing.T) {
	type SpecRaw struct {
		Flag   RawTerm `etf:"flag:atom => boolean"`
		Nums   RawTerm `etf:"nums:atom => list(integer)"`
		Atoms  RawTerm `etf:"atoms:atom => list(atom)"`
		Matrix RawTerm `etf:"matrix:atom => list(list(integer))"`
		Bin    RawTerm `etf:"bin:atom => binary"`
		Chars  RawTerm `etf:"chars:atom => charlist"`
	}

	// Valid terms pass both decode and encode.
	validBool, _ := Marshal(true)
	validNums, _ := Marshal([]int{1, 2, 3})
	validAtoms, _ := Marshal([]Atom{"a", "b"})
	validMatrix, _ := Marshal([][]int{{1, 2}, {3}})
	validBit8 := RawTerm{VersionMagic, TagBitBinary, 0, 0, 0, 2, 8, 'o', 'k'}
	validChars, _ := Marshal(Charlist("hé"))

	in := SpecRaw{
		Flag:   validBool,
		Nums:   validNums,
		Atoms:  validAtoms,
		Matrix: validMatrix,
		Bin:    validBit8,
		Chars:  validChars,
	}
	encoded, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal valid SpecRaw failed: %v", err)
	}
	var out SpecRaw
	if err := Unmarshal(encoded, &out); err != nil {
		t.Fatalf("Unmarshal valid SpecRaw failed: %v", err)
	}

	// Invalid terms rejected on BOTH Unmarshal and Marshal.
	maybeAtom, _ := Marshal(Atom("maybe"))
	badNums, _ := Marshal([]any{int64(1), Atom("oops")})
	strExtBytes := RawTerm{VersionMagic, TagString, 0, 2, 'h', 'i'}
	badMatrix, _ := Marshal([]any{[]any{int64(1)}, []any{Atom("bad")}})
	badBit3 := RawTerm{VersionMagic, TagBitBinary, 0, 0, 0, 1, 3, 0xE0}
	badCharlist, _ := Marshal([]any{Atom("not_rune")})
	improperCharlist, _ := Marshal(ImproperList{Elements: []any{int64('a')}, Tail: Atom("bad")})

	makeMap := func(k string, term RawTerm) []byte {
		out := []byte{VersionMagic, TagMap, 0, 0, 0, 1, TagSmallAtomUTF8, byte(len(k))}
		out = append(out, k...)
		out = append(out, term[1:]...)
		return out
	}

	cases := []struct {
		name    string
		key     string
		term    RawTerm
		encVal  SpecRaw
		wantErr error
	}{
		{"boolean_maybe", "flag", maybeAtom, SpecRaw{Flag: maybeAtom}, ErrTypeMismatch},
		{"list_integer_bad_elem", "nums", badNums, SpecRaw{Nums: badNums}, ErrTypeMismatch},
		{"list_atom_string_ext", "atoms", strExtBytes, SpecRaw{Atoms: strExtBytes}, ErrTypeMismatch},
		{"nested_list_bad_elem", "matrix", badMatrix, SpecRaw{Matrix: badMatrix}, ErrTypeMismatch},
		{"binary_non_aligned_bits", "bin", badBit3, SpecRaw{Bin: badBit3}, ErrTypeMismatch},
		{"charlist_atom_elem", "chars", badCharlist, SpecRaw{Chars: badCharlist}, ErrTypeMismatch},
		{"charlist_improper_tail", "chars", improperCharlist, SpecRaw{Chars: improperCharlist}, ErrImproperList},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst SpecRaw
			if err := Unmarshal(makeMap(tc.key, tc.term), &dst); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Unmarshal %s: expected %v, got %v", tc.name, tc.wantErr, err)
			}
			if _, err := Marshal(tc.encVal); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Marshal %s: expected %v, got %v", tc.name, tc.wantErr, err)
			}
		})
	}
}

func TestBitBinaryMapKeyInStruct(t *testing.T) {
	type S struct {
		ID int `etf:"id:binary => integer, required"`
	}

	// Byte-aligned BIT_BINARY_EXT (Bits=8) matches binary struct field key.
	wireBit8Key := []byte{
		VersionMagic, TagMap, 0, 0, 0, 1,
		TagBitBinary, 0, 0, 0, 2, 8, 'i', 'd',
		TagSmallInt, 42,
	}
	var dst S
	if err := Unmarshal(wireBit8Key, &dst, WithDisallowUnknownFields()); err != nil {
		t.Fatalf("Unmarshal byte-aligned BIT_BINARY_EXT key failed: %v", err)
	}
	if dst.ID != 42 {
		t.Fatalf("expected ID=42, got %d", dst.ID)
	}

	// Byte-aligned BIT_BINARY_EXT key collides with BINARY_EXT key as duplicate.
	wireDup := []byte{
		VersionMagic, TagMap, 0, 0, 0, 2,
		TagBinary, 0, 0, 0, 2, 'i', 'd', TagSmallInt, 1,
		TagBitBinary, 0, 0, 0, 2, 8, 'i', 'd', TagSmallInt, 2,
	}
	if err := Unmarshal(wireDup, &dst); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for BINARY_EXT + byte-aligned BIT_BINARY_EXT key, got %v", err)
	}

	// Non-byte-aligned BIT_BINARY_EXT (Bits=3) does not match binary key and is unknown.
	wireBit3Key := []byte{
		VersionMagic, TagMap, 0, 0, 0, 2,
		TagBinary, 0, 0, 0, 2, 'i', 'd', TagSmallInt, 7,
		TagBitBinary, 0, 0, 0, 1, 3, 0xE0, TagSmallInt, 9,
	}
	if err := Unmarshal(wireBit3Key, &dst, WithDisallowUnknownFields()); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField for non-byte-aligned bitstring key, got %v", err)
	}
	if err := Unmarshal(wireBit3Key, &dst); err != nil || dst.ID != 7 {
		t.Fatalf("expected non-byte-aligned bitstring key to be skipped when unknown fields allowed, got dst=%+v err=%v", dst, err)
	}
}

func TestLargeMapEqualAndComplexKeyDuplicateTracking(t *testing.T) {
	// Build two large maps (500 entries) with mixed scalar + complex keys in opposite orders.
	const n = 500
	m1 := make(Map, 0, n+4)
	for i := 0; i < n; i++ {
		m1 = append(m1, MapEntry{
			Key:   []any{Atom("k"), int64(i)},
			Value: Map{{Key: Atom("v"), Value: int64(i)}},
		})
	}
	// Add cross-representation equivalent keys to verify hashTerm == invariant with Equal.
	m1 = append(m1,
		MapEntry{Key: Charlist("AB"), Value: Binary("charlist_key")},
		MapEntry{Key: Bitstring{Data: []byte("hi"), Bits: 8}, Value: int64(1)},
		MapEntry{Key: Bitstring{Data: []byte{0xFF}, Bits: 3}, Value: int64(2)},
		MapEntry{Key: Map{{Key: Atom("a"), Value: int64(1)}, {Key: Atom("b"), Value: int64(2)}}, Value: true},
	)

	m2 := make(Map, 0, len(m1))
	for i := n - 1; i >= 0; i-- {
		m2 = append(m2, MapEntry{
			Key:   []any{Atom("k"), int64(i)},
			Value: Map{{Key: Atom("v"), Value: int64(i)}},
		})
	}
	m2 = append(m2,
		MapEntry{Key: []any{int64(65), uint8(66)}, Value: "charlist_key"},
		MapEntry{Key: Binary("hi"), Value: big.NewInt(1)},
		MapEntry{Key: Bitstring{Data: []byte{0xE0}, Bits: 3}, Value: int64(2)},
		MapEntry{Key: Map{{Key: Atom("b"), Value: int64(2)}, {Key: Atom("a"), Value: int64(1)}}, Value: Atom("true")},
	)

	if !Equal(m1, m2) || !Equal(m2, m1) {
		t.Fatal("expected large maps with equivalent complex keys in different orders to be Equal")
	}

	// Mutate a single value -> Equal must return false.
	m2[0].Value = Map{{Key: Atom("v"), Value: int64(-1)}}
	if Equal(m1, m2) {
		t.Fatal("expected Equal to return false when one value differs")
	}

	// Duplicate complex map keys (reordered inner maps) rejected via hash-bucketed dupKeyTracker.
	dupComplex := Map{
		{Key: Map{{Key: Atom("a"), Value: int64(1)}, {Key: Atom("b"), Value: int64(2)}}, Value: int64(10)},
		{Key: Map{{Key: Atom("b"), Value: int64(2)}, {Key: Atom("a"), Value: int64(1)}}, Value: int64(20)},
	}
	rawDup, err := Marshal(dupComplex)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Map
	if err := Unmarshal(rawDup, &decoded); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for duplicate reordered nested map keys, got %v", err)
	}
}

func TestSkipUnsupportedTermsInIgnoredStructFields(t *testing.T) {
	// Real OTP 27 wire encodings (without leading VersionMagic 131).
	newPid := []byte{
		88, TagSmallAtomUTF8, 13, 'n', 'o', 'n', 'o', 'd', 'e', '@', 'n', 'o', 'h', 'o', 's', 't',
		0, 0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	newPidLegacyAtom := []byte{
		88, 100, 0, 13, 'n', 'o', 'n', 'o', 'd', 'e', '@', 'n', 'o', 'h', 'o', 's', 't',
		0, 0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	newerRef := []byte{
		90, 0, 3,
		TagSmallAtomUTF8, 13, 'n', 'o', 'n', 'o', 'd', 'e', '@', 'n', 'o', 'h', 'o', 's', 't',
		0, 0, 0, 0,
		0, 0, 119, 85, 200, 84, 0, 1, 157, 162, 39, 254,
	}
	v4Port := []byte{
		120, TagSmallAtomUTF8, 13, 'n', 'o', 'n', 'o', 'd', 'e', '@', 'n', 'o', 'h', 'o', 's', 't',
		0, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 0,
	}
	exportExt := []byte{
		113,
		TagSmallAtomUTF8, 5, 'l', 'i', 's', 't', 's',
		TagSmallAtomUTF8, 7, 'r', 'e', 'v', 'e', 'r', 's', 'e',
		TagSmallInt, 1,
	}
	// Minimal valid NEW_FUN_EXT (Size = 29 + 4 bytes of dummy body = 33).
	newFun := make([]byte, 1+33)
	newFun[0] = 112
	binary.BigEndian.PutUint32(newFun[1:5], 33)
	// LOCAL_EXT wrapping NEWER_REFERENCE_EXT with NIL_EXT node (real OTP 27 layout).
	localRef := []byte{
		121, 27, 213, 79, 161,
		90, 0, 3, TagNil,
		0, 0, 0, 0,
		0, 0, 119, 85, 200, 84, 0, 1, 157, 162, 39, 254,
	}
	// Legacy opaque terms: PID_EXT(103), PORT_EXT(102), NEW_PORT_EXT(89), REFERENCE_EXT(101), NEW_REFERENCE_EXT(114), FUN_EXT(117), ATOM_CACHE_REF(82), FLOAT_EXT(99), SMALL_ATOM_EXT(115).
	legacyPid := []byte{103, TagSmallAtomUTF8, 1, 'n', 0, 0, 0, 1, 0, 0, 0, 2, 3}
	legacyPort := []byte{102, TagSmallAtomUTF8, 1, 'n', 0, 0, 0, 1, 2}
	newPort := []byte{89, TagSmallAtomUTF8, 1, 'n', 0, 0, 0, 1, 0, 0, 0, 2}
	legacyRef := []byte{101, TagSmallAtomUTF8, 1, 'n', 0, 0, 0, 1, 2}
	legacyNewRef := []byte{114, 0, 1, TagSmallAtomUTF8, 1, 'n', 2, 0, 0, 0, 9}
	legacyFun := []byte{117, 0, 0, 0, 1, TagSmallInt, 1, TagSmallAtomUTF8, 1, 'm', TagSmallInt, 2, TagSmallInt, 3, TagSmallInt, 4}
	atomCacheRef := []byte{82, 7}
	legacyFloat := append([]byte{99}, make([]byte, 31)...)
	legacySmallAtom := []byte{115, 2, 'o', 'k'}
	nestedContainer := append([]byte{TagSmallTuple, 2, TagSmallAtomUTF8, 3, 'p', 'i', 'd'}, newPid...)

	type Target struct {
		ID      int     `etf:"id:atom => integer, required"`
		Name    string  `etf:"name:atom => binary, required"`
		Ignored string  `etf:"-"`
		RawPid  RawTerm `etf:"raw_pid:atom"`
	}

	atomKey := func(s string) []byte {
		return append([]byte{TagSmallAtomUTF8, byte(len(s))}, s...)
	}
	intVal := func(v byte) []byte { return []byte{TagSmallInt, v} }
	binVal := func(s string) []byte {
		b := []byte{TagBinary, 0, 0, 0, byte(len(s))}
		return append(b, s...)
	}

	entries := [][2][]byte{
		{atomKey("pid"), newPid},
		{atomKey("pid_v1"), newPidLegacyAtom},
		{atomKey("ref"), newerRef},
		{atomKey("port"), v4Port},
		{atomKey("export"), exportExt},
		{atomKey("fun"), newFun},
		{atomKey("local"), localRef},
		{atomKey("legacy_pid"), legacyPid},
		{atomKey("legacy_port"), legacyPort},
		{atomKey("new_port"), newPort},
		{atomKey("legacy_ref"), legacyRef},
		{atomKey("legacy_new_ref"), legacyNewRef},
		{atomKey("legacy_fun"), legacyFun},
		{atomKey("cache_ref"), atomCacheRef},
		{atomKey("legacy_float"), legacyFloat},
		{atomKey("legacy_atom"), legacySmallAtom},
		{atomKey("nested"), nestedContainer},
		{atomKey("ignored"), newPid},
		// Unknown keys that are themselves unsupported or contain unsupported tags!
		{newPid, binVal("pid_key_val")},
		{newerRef, intVal(1)},
		{nestedContainer, binVal("tuple_with_pid_key")},
		{legacySmallAtom, intVal(2)},
		// Actual required/matched fields interspersed & at end!
		{atomKey("id"), intVal(42)},
		{atomKey("name"), binVal("worker")},
		{atomKey("raw_pid"), newPid},
	}

	buildMap := func(pairs [][2][]byte) []byte {
		var buf bytes.Buffer
		buf.WriteByte(VersionMagic)
		buf.WriteByte(TagMap)
		var arity [4]byte
		binary.BigEndian.PutUint32(arity[:], uint32(len(pairs)))
		buf.Write(arity[:])
		for _, kv := range pairs {
			buf.Write(kv[0])
			buf.Write(kv[1])
		}
		return buf.Bytes()
	}

	wire := buildMap(entries)

	// 1. Unmarshal into Target succeeds and preserves RawPid verbatim.
	var got Target
	if err := Unmarshal(wire, &got); err != nil {
		t.Fatalf("Unmarshal with ignored unsupported terms failed: %v", err)
	}
	wantRawPid := append([]byte{VersionMagic}, newPid...)
	if got.ID != 42 || got.Name != "worker" || got.Ignored != "" || !bytes.Equal(got.RawPid, wantRawPid) {
		t.Fatalf("unexpected decoded Target: %+v", got)
	}

	// 2. Streaming Decoder frames and decodes the same map cleanly.
	var streamGot Target
	if err := NewDecoder(bytes.NewReader(wire)).Decode(&streamGot); err != nil {
		t.Fatalf("streaming Decoder with ignored unsupported terms failed: %v", err)
	}
	if streamGot.ID != 42 || streamGot.Name != "worker" {
		t.Fatalf("unexpected stream Target: %+v", streamGot)
	}

	// 3. WithDisallowUnknownFields still rejects unknown NEW_PID_EXT fields.
	var strictGot Target
	if err := Unmarshal(wire, &strictGot, WithDisallowUnknownFields()); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField with WithDisallowUnknownFields, got %v", err)
	}

	// 4. Matched struct field with NEW_PID_EXT value STILL fails (ErrTypeMismatch on typed, ErrUnsupportedTag on untyped).
	type UntypedID struct {
		ID any `etf:"id:atom, required"`
	}
	pidInRequired := buildMap([][2][]byte{{atomKey("id"), newPid}})
	var uid UntypedID
	if err := Unmarshal(pidInRequired, &uid); !errors.Is(err, ErrUnsupportedTag) {
		t.Fatalf("expected ErrUnsupportedTag for matched untyped field with NEW_PID_EXT, got %v", err)
	}
	var typedDst Target
	if err := Unmarshal(pidInRequired, &typedDst); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for matched typed field with NEW_PID_EXT, got %v", err)
	}

	// 5. Duplicate NEW_PID_EXT keys in a map are still caught as ErrDuplicateMapKey.
	dupPidKeyWire := buildMap([][2][]byte{
		{newPid, intVal(1)},
		{newPid, intVal(2)},
		{atomKey("id"), intVal(1)},
		{atomKey("name"), binVal("ok")},
	})
	var dupDst Target
	if err := Unmarshal(dupPidKeyWire, &dupDst); !errors.Is(err, ErrDuplicateMapKey) {
		t.Fatalf("expected ErrDuplicateMapKey for duplicate NEW_PID_EXT keys, got %v", err)
	}

	// 6. Truncated NEW_PID_EXT and bogus tag byte (255) in ignored fields still fail cleanly.
	truncWire := buildMap([][2][]byte{
		{atomKey("bad_pid"), newPid[:len(newPid)-3]},
	})
	if err := Unmarshal(truncWire, &dupDst); !errors.Is(err, ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF for truncated ignored NEW_PID_EXT, got %v", err)
	}
	bogusWire := buildMap([][2][]byte{
		{atomKey("bogus"), {255}},
	})
	if err := Unmarshal(bogusWire, &dupDst); !errors.Is(err, ErrUnsupportedTag) {
		t.Fatalf("expected ErrUnsupportedTag for unknown tag 255 in ignored field, got %v", err)
	}
}
