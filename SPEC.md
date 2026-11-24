# `etf` Struct Tag Specification

Every exported struct field in `package etf` maps to an Erlang map entry with an explicit Erlang key type and an optional Erlang value type constraint.

Wire encoding follows the [Erlang External Term Format (ETF)](https://www.erlang.org/doc/apps/erts/erl_ext_dist.html) specification.

## Syntax

```text
`etf:"-"`
`etf:"<name>:<key_type>[ => <value_type>][, required][, null=<atom>]"`
```

- Every exported, non-embedded struct field **must** declare an `etf` tag (or `etf:"-"` to ignore it). Omitting the tag returns `*InvalidTagError`.
- Unexported non-embedded fields are ignored.
- `<name>:<key_type>` must appear first (split on the last `:` before `=>` or `,`, so `user:id:binary` works).
- `<name>` cannot be empty and cannot contain whitespace, control characters, parentheses (`(` / `)`), quotes (`'` / `"`), backslashes (`\`), commas (`,`), or `=>`. Use `etf.Map` or Go maps for arbitrary keys outside struct tags.
- `=> <value_type>` is optional. Whitespace around `=>` and `,` is optional.
- Comma-separated options (`required`, `null=<atom>`) may appear in any order after `<key_type>` (or `=> <value_type>`). Empty, duplicate, or unknown options return `*InvalidTagError`.

## Key Types (`:<key_type>`, mandatory)

Every tagged field must start with `<name>:<key_type>`.

| `<key_type>` | `<name>` validation | ETF wire encoding |
|---|---|---|
| `atom` | Valid UTF-8, ≤ 255 Unicode runes | `SMALL_ATOM_UTF8_EXT` / `ATOM_UTF8_EXT` |
| `binary` | Any byte sequence | `BINARY_EXT` |
| `charlist` | Valid UTF-8 | `NIL_EXT` / `STRING_EXT` / `LIST_EXT` |
| `integer` | Valid base-10 integer (arbitrary precision) | `SMALL_INTEGER_EXT` / `INTEGER_EXT` / `SMALL_BIG_EXT` / `LARGE_BIG_EXT` |
| `float` | Finite IEEE-754 float64 (`NaN`/`Inf` rejected) | `NEW_FLOAT_EXT` |
| `boolean` | Strictly `"true"` or `"false"` | Atom `"true"` / `"false"` |

### Key Identity (Erlang `=:=`)

Struct keys follow strict Erlang term identity:
- Different key types with the same name are distinct keys (e.g. `user_id:atom` and `user_id:binary` can coexist on the same struct).
- `boolean` keys share wire identity with `atom` keys `"true"`/`"false"`; declaring both on the same struct returns `*InvalidTagError`.
- `integer` keys are canonicalized by numeric value (`01` and `1` conflict).
- `float` keys compare by IEEE-754 bit pattern (`-0.0` and `+0.0` are distinct per OTP 27).

## Value Types (optional)

At most one value type expression may be specified per field. When omitted, encoding uses the Go type's default ETF wire type and decoding permits safe coercion (unless `WithStrictTypes()` is set).

| Value type | Allowed ETF wire tags on decode | Behavior on encode |
|---|---|---|
| `integer` | `SMALL_INTEGER_EXT`, `INTEGER_EXT`, `SMALL_BIG_EXT`, `LARGE_BIG_EXT` | Encodes Go ints, uints, `*big.Int`, or exact-integer floats (fractional floats fail with `ErrLossyCoercion`) |
| `float` | `NEW_FLOAT_EXT` | Encodes Go floats, ints, uints, `*big.Int` as `NEW_FLOAT_EXT` (rejects `NaN`/`Inf` with `ErrNonFiniteFloat`) |
| `number` | Any integer or float tag | Encodes ints/uints/`*big.Int` as integer; encodes floats with a `.0` fractional part (`math.Trunc(f) == f`) as integer and fractional floats as `NEW_FLOAT_EXT` |
| `atom` | `SMALL_ATOM_UTF8_EXT`, `ATOM_UTF8_EXT` | Encodes `string`, `bool`, `[]byte` as UTF-8 atom (≤ 255 runes) |
| `boolean` | Atom tags with value `"true"` or `"false"` only | Encodes `bool` or `"true"`/`"false"` string |
| `binary` | `BINARY_EXT`, or byte-aligned `BIT_BINARY_EXT` (`Bits=8`) | Encodes `string`, `[]byte`, or byte-aligned `Bitstring` as `BINARY_EXT` |
| `bitstring` | `BIT_BINARY_EXT`, `BINARY_EXT` | Encodes `Bitstring` (preserving bit count), `string`, or `[]byte` |
| `charlist` | `NIL_EXT`, `STRING_EXT`, `LIST_EXT` (valid Unicode code points, `NIL_EXT` tail) | Encodes `string`, `[]byte`, or `[]rune` (valid Unicode code points) |
| `list` | `NIL_EXT`, `STRING_EXT`, `LIST_EXT` | Encodes slice, array, or `ImproperList` |
| `list(T)` | Same as `list`, validating every element against `T` (including `STRING_EXT` byte elements) | Encodes each element with spec `T` |
| `map` | `MAP_EXT` | Encodes `etf.Map`, Go `map[K]V`, or Go `struct` |
| `tuple` | `SMALL_TUPLE_EXT`, `LARGE_TUPLE_EXT` | Requires a Go type implementing both `etf.Marshaler` and `etf.Unmarshaler` (e.g. custom type, `etf.Tuple`, `etf.DateTime`, `etf.RawTerm`); otherwise returns `*InvalidTagError` |
| `datetime` | `SMALL_TUPLE_EXT`, `LARGE_TUPLE_EXT` matching validated `{{year(), month(), day()}, {hour(), minute(), second()}}` | Requires a Go type implementing both `etf.Marshaler` and `etf.Unmarshaler` (e.g. `etf.DateTime`, `etf.RawTerm`, `etf.Tuple`); otherwise returns `*InvalidTagError` |

Only `list` accepts an element type parameter (`list(T)`), which nests arbitrarily (e.g. `list(list(integer))`, `list(map)`, `list(tuple)`, `list(datetime)`). `map` does not accept key/value type parameters (`map(K=>V)` is rejected with `*InvalidTagError`); use Go map key/value types such as `etf.Atom`, `etf.Binary`, or structs instead.

## Flags & Options

| Option | Effect |
|---|---|
| `required` | Decode returns `ErrMissingRequiredField` if the key is absent from the incoming map (a present key whose value is the nil atom satisfies `required` for pointer fields). Encode always emits the field, including `nil` pointers/interfaces (as the nil atom) and `nil` slices/maps. When `required` is omitted, encode omits `nil` pointers, interfaces, slices, and maps, while non-nil zero values (`0`, `false`, `""`, empty non-nil slices/maps) are still encoded. |
| `null=<atom>` | Overrides the nil atom for this field (default `undefined`, or `WithNilAtom`/`WithDecodeNilAtom`). Must be valid UTF-8 and ≤ 255 Unicode runes. Used when encoding `nil` pointers/interfaces on `required` fields and when decoding into pointer fields. |

## Embedded Structs

- Anonymous embedded structs (and pointers to structs) **without** an `etf` tag have their tagged fields promoted into the enclosing struct's map.
- Anonymous embedded structs **with** an `etf` tag are encoded/decoded as regular nested map fields under that tag's key.
- Shallower embedding depth shadows deeper fields with the same Erlang map key. Duplicate keys at the same depth return `*InvalidTagError`.
- On encode, fields behind a `nil` embedded struct pointer are skipped.
- On decode, a `nil` exported embedded struct pointer is allocated automatically. A `nil` unexported embedded struct pointer cannot be set via reflection and returns a `*DecodeError` (pre-initialized non-nil or value-embedded unexported structs decode normally).

## Default Go Type Mapping & Coercion

When a field omits `<value_type>` (or when marshaling/unmarshaling outside a struct):

| Go type | Default encode wire type | Default decode accepts |
|---|---|---|
| `int*`, `uint*`, `*big.Int` | Integer | Integer (bounds-checked -> `ErrOverflow`), exact-integer float (fractional -> `ErrLossyCoercion`) |
| `float32`, `float64` | `NEW_FLOAT_EXT` | Float, Integer (bounds-checked) |
| `bool` | Atom `true`/`false` | Atom `true`/`false` |
| `string` | `BINARY_EXT` | Binary, byte-aligned Bitstring, Atom, Charlist (`NIL_EXT`/`STRING_EXT`/`LIST_EXT`) |
| `[]byte` | `BINARY_EXT` | Binary, byte-aligned Bitstring, Atom, `NIL_EXT`, `STRING_EXT` |
| `etf.Atom` | Atom | Atom only |
| `etf.Binary` | `BINARY_EXT` | Binary, byte-aligned Bitstring only |
| `etf.Charlist` | Charlist | `NIL_EXT`, `STRING_EXT`, `LIST_EXT` (code points) only |
| `etf.Bitstring` | `BIT_BINARY_EXT` / `BINARY_EXT` | `BIT_BINARY_EXT`, `BINARY_EXT` |
| `etf.RawTerm` | Passthrough ETF term | Any valid ETF term verbatim (enforces `<value_type>` if declared; pointer `*RawTerm` still decodes `null` atom as `nil`) |
| `[]T`, `[N]T` | `NIL_EXT` / `LIST_EXT` | `NIL_EXT`, `STRING_EXT`, proper `LIST_EXT` (improper -> `ErrImproperList`) |
| `etf.ImproperList` | `LIST_EXT` with custom tail | `NIL_EXT`, `LIST_EXT` (proper or improper) |
| `etf.Tuple` | `SMALL_TUPLE_EXT` / `LARGE_TUPLE_EXT` | `SMALL_TUPLE_EXT`, `LARGE_TUPLE_EXT` |
| `etf.DateTime` | `{{Y, M, D}, {H, Min, S}}` (`SMALL_TUPLE_EXT`) | Validated Erlang datetime tuple (`ErrInvalidDateTime` on invalid structure/entries) |
| `map[K]V` | `MAP_EXT` | `MAP_EXT` (non-comparable keys -> `ErrUncomparableMapKey`) |
| `etf.Map` | `MAP_EXT` | `MAP_EXT` (supports arbitrary Erlang keys) |
| `any` | Depends on dynamic value | `int64`/`*big.Int`, `float64`, `bool`, `Atom`, `Binary`, `Bitstring`, `[]any`, `ImproperList`, `Tuple`, `Map` |

With `WithStrictTypes()`, cross-type coercions for untyped fields and standalone values (e.g. int↔float, atom→`string`/`[]byte`, charlist/`NIL_EXT`→`string`/`[]byte`) are rejected with `ErrTypeMismatch`. Explicit wrapper types (`Atom`, `Binary`, `Charlist`) always enforce their wire type regardless of `WithStrictTypes()`.

## Go Enum-Like Constants

Defined scalar types (e.g. `iota` integer enums, `string` enums, `bool` enums) are supported automatically via their underlying Go kind, including in slices, maps, and pointers:

```go
type OrderStatus int
const (
    OrderPending OrderStatus = iota
    OrderPaid
)

type PaymentMethod string
const (
    PaymentCard PaymentMethod = "card"
    PaymentSEPA PaymentMethod = "sepa"
)

// Type alias to etf.Atom encodes/decodes as an ETF atom even without an `atom` tag.
type Currency = etf.Atom
const CurrencyEUR Currency = "EUR"

type PaymentRequest struct {
    Status      OrderStatus              `etf:"status:atom => integer, required"`
    Method      PaymentMethod            `etf:"method:atom => atom, required"`
    Allowed     []PaymentMethod          `etf:"allowed_methods:atom => list(atom)"`
    ByCurrency  map[Currency]OrderStatus `etf:"by_currency:atom => map"`
    Currency    Currency                 `etf:"currency:atom, required"`
}
```

Notes:
- Defined `string` enums default to `BINARY_EXT` when no value type is specified; add `atom` (or `list(atom)`) in the struct tag to encode/decode them as Erlang atoms, or use a type alias (`type Currency = etf.Atom`) for atom wire typing everywhere (including Go map keys).
- Narrow integer enums (e.g. `type RiskLevel uint8`) enforce bounds checks (`ErrOverflow`) on decode. For slices of `uint8`-based enums, specify `list(integer)` if you want an ETF list instead of `BINARY_EXT`.

## Tuples & Erlang DateTime

- **Tuples**: Erlang tuples (`SMALL_TUPLE_EXT` / `LARGE_TUPLE_EXT`) are never auto-mapped to plain Go structs, slices, or arrays. Any struct field tagged `=> tuple` (or nested inside `list(tuple)`) must use a type implementing both `etf.Marshaler` and `etf.Unmarshaler` (such as a domain-specific custom type, `etf.DateTime`, `etf.RawTerm`, or `etf.Tuple`). Use `etf.Tuple` (`[]any`) for dynamic values (`any`), arbitrary map keys, or inside custom `MarshalETF`/`UnmarshalETF` implementations.
- **Ignored / Unknown Struct Fields**: Unless `WithDisallowUnknownFields()` is set, map entries not matched by any struct field (including fields ignored with `etf:"-"`) are skipped safely even when their keys or values contain ETF types the codec does not materialize (such as `NEW_PID_EXT`, `NEWER_REFERENCE_EXT`, `V4_PORT_EXT`, `NEW_FUN_EXT`, `EXPORT_EXT`, `LOCAL_EXT`, or legacy atom/float tags). Decoding into a matched/required field with an unsupported tag still returns `ErrUnsupportedTag` (or `ErrTypeMismatch`).
- **Erlang DateTime**: `etf.DateTime` implements `Marshaler`/`Unmarshaler` for Erlang `calendar:datetime()` (`{{year(), month(), day()}, {hour(), minute(), second()}}`). Both `Marshal` and `Unmarshal` (as well as `=> datetime` tag validation) enforce:
  - `Year >= 0`
  - `Month` in `1..12`
  - `Day` in `1..daysInMonth(Year, Month)` (Gregorian leap-year aware)
  - `Hour` in `0..23`, `Minute` in `0..59`, `Second` in `0..59`
  - All 6 entries must be integers (no floats/atoms/binaries); violations return `ErrInvalidDateTime`.

## Errors

- Tag parsing/schema errors return `*InvalidTagError`.
- Decode failures wrap sentinel errors in `*DecodeError` (with `Path`, `Tag`, `Type`), inspectable via `errors.Is`:
  `ErrTypeMismatch`, `ErrMissingRequiredField`, `ErrUnknownField`, `ErrOverflow`, `ErrLossyCoercion`, `ErrDuplicateMapKey`, `ErrUncomparableMapKey`, `ErrImproperList`, `ErrInvalidBitstring`, `ErrInvalidDateTime`, `ErrNonFiniteFloat`, `ErrAtomTooLong`, `ErrInvalidUTF8`, `ErrMaxDepthExceeded`, `ErrMaxSizeExceeded`.
