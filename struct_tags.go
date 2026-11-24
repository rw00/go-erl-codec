package etf

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type valueKind uint8

const (
	valAny valueKind = iota // omitted in tag -> inferred/coerced by Go type
	valInteger
	valFloat
	valNumber
	valAtom
	valBoolean
	valBinary
	valBitstring
	valCharlist
	valList
	valMap
	valTuple
	valDateTime
)

type typeSpec struct {
	kind    valueKind
	elem    *typeSpec // for list(T)
	rawText string
}

type keyKind uint8

const (
	keyKindAtom keyKind = iota + 1 // also covers boolean atoms true/false
	keyKindBinary
	keyKindCharlist
	keyKindInteger
	keyKindFloat
	keyKindString // Go string map key (decodeMapIntoGoMap duplicate tracking)
)

// structKey uniquely identifies an Erlang scalar map key using strict Erlang =:= identity.
type structKey struct {
	kind      keyKind
	str       string // atom name, binary bytes, charlist UTF-8, or canonical base-10 integer
	floatBits uint64 // IEEE-754 bits for float keys (distinguishes -0.0 from +0.0)
}

func (sk structKey) display() string {
	if sk.kind == keyKindFloat {
		return strconv.FormatFloat(math.Float64frombits(sk.floatBits), 'g', -1, 64)
	}
	return sk.str
}

type parsedMapKey struct {
	keyType  KeyType
	canon    structKey
	rawName  string
	intVal   *big.Int
	floatVal float64
	boolVal  bool
}

type fieldInfo struct {
	name       string
	index      []int
	typ        reflect.Type
	tag        string
	key        parsedMapKey
	keyWire    []byte
	valSpec    *typeSpec
	required   bool
	hasNilAtom bool
	nilAtom    Atom
}

type structInfo struct {
	typ            reflect.Type
	fields         []fieldInfo
	byKey          map[structKey]int // index into fields
	hasRequired    bool
	requiredMask   uint64 // bit i set if fields[i].required (used when len(fields) <= 64)
	hasCharlistKey bool
}

type structCacheEntry struct {
	info *structInfo
	err  error
}

var structCache sync.Map // map[reflect.Type]structCacheEntry

func getStructInfo(t reflect.Type) (*structInfo, error) {
	if v, ok := structCache.Load(t); ok {
		entry := v.(structCacheEntry)
		return entry.info, entry.err
	}
	info, err := buildStructInfo(t)
	structCache.Store(t, structCacheEntry{info: info, err: err})
	return info, err
}

type candidateField struct {
	info       fieldInfo
	depth      int
	dupAtDepth bool
}

func buildStructInfo(root reflect.Type) (*structInfo, error) {
	type queueItem struct {
		typ   reflect.Type
		index []int
	}

	var candidates []candidateField
	visited := make(map[reflect.Type]bool)
	current := []queueItem{{typ: root, index: nil}}
	count := map[reflect.Type]int{root: 1}

	for depth := 0; len(current) > 0; depth++ {
		var next []queueItem
		nextCount := make(map[reflect.Type]int)

		for _, curr := range current {
			if visited[curr.typ] {
				continue
			}
			visited[curr.typ] = true
			currCount := count[curr.typ]
			ambiguous := currCount > 1

			for i := 0; i < curr.typ.NumField(); i++ {
				sf := curr.typ.Field(i)
				isUnexported := sf.PkgPath != ""

				tagVal, hasTag := sf.Tag.Lookup("etf")

				// Anonymous embedded structs (without an explicit etf tag) are promoted.
				if sf.Anonymous && !hasTag {
					ft := sf.Type
					if ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					if ft.Kind() == reflect.Struct {
						if !visited[ft] {
							if nextCount[ft] == 0 {
								idx := make([]int, len(curr.index)+1)
								copy(idx, curr.index)
								idx[len(curr.index)] = i
								next = append(next, queueItem{typ: ft, index: idx})
							}
							if nextCount[ft] < 2 {
								nextCount[ft] += currCount
								if nextCount[ft] > 2 {
									nextCount[ft] = 2
								}
							}
						}
						continue
					}
					if isUnexported {
						continue
					}
				}

				if isUnexported {
					continue
				}

				if !hasTag {
					return nil, &InvalidTagError{
						Struct: root,
						Field:  sf.Name,
						Tag:    "",
						Reason: `missing etf tag; specify "<name>:<key_type>" or "-" to ignore`,
					}
				}
				if tagVal == "-" {
					continue
				}

				idx := make([]int, len(curr.index)+1)
				copy(idx, curr.index)
				idx[len(curr.index)] = i

				fi, err := parseFieldTag(root, sf, idx, tagVal)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, candidateField{info: fi, depth: depth, dupAtDepth: ambiguous})
			}
		}

		current = next
		count = nextCount
	}

	// Resolve shadowing across embedding depths (shallower depth wins; duplicates at same depth error).
	byKey := make(map[structKey]candidateField, len(candidates))
	var ordered []structKey

	for _, cand := range candidates {
		k := cand.info.key.canon
		prev, exists := byKey[k]
		if !exists {
			if cand.dupAtDepth {
				return nil, &InvalidTagError{
					Struct: root,
					Field:  cand.info.name,
					Tag:    cand.info.tag,
					Reason: fmt.Sprintf("duplicate Erlang map key (%s %q) conflicts with field %s", cand.info.key.keyType, cand.info.key.rawName, cand.info.name),
				}
			}
			byKey[k] = cand
			ordered = append(ordered, k)
			continue
		}
		if cand.depth == prev.depth {
			return nil, &InvalidTagError{
				Struct: root,
				Field:  cand.info.name,
				Tag:    cand.info.tag,
				Reason: fmt.Sprintf("duplicate Erlang map key (%s %q) conflicts with field %s", cand.info.key.keyType, cand.info.key.rawName, prev.info.name),
			}
		}
		if cand.depth < prev.depth {
			byKey[k] = cand
		}
	}

	si := &structInfo{
		typ:    root,
		fields: make([]fieldInfo, 0, len(ordered)),
		byKey:  make(map[structKey]int, len(ordered)),
	}
	for _, k := range ordered {
		idx := len(si.fields)
		fi := byKey[k].info
		si.byKey[k] = idx
		si.fields = append(si.fields, fi)
		if fi.required {
			si.hasRequired = true
			if idx < 64 {
				si.requiredMask |= 1 << uint(idx)
			}
		}
		if fi.key.keyType == KeyCharlist {
			si.hasCharlistKey = true
		}
	}
	return si, nil
}

func parseFieldTag(structType reflect.Type, sf reflect.StructField, index []int, tag string) (fieldInfo, error) {
	fail := func(reason string) (fieldInfo, error) {
		return fieldInfo{}, &InvalidTagError{
			Struct: structType,
			Field:  sf.Name,
			Tag:    tag,
			Reason: reason,
		}
	}

	keyPart, rest := splitTagKeyPart(tag)
	colonIdx := strings.LastIndexByte(keyPart, ':')
	if colonIdx < 0 {
		return fail(`missing "<name>:<key_type>" prefix`)
	}
	keyName := keyPart[:colonIdx]
	if keyName == "" {
		return fail("key name cannot be empty")
	}
	if err := validateTagKeyName(keyName); err != nil {
		return fail(err.Error())
	}
	keyType, err := parseKeyType(keyPart[colonIdx+1:])
	if err != nil {
		return fail(err.Error())
	}

	var (
		valSpec    *typeSpec
		required   bool
		hasNilAtom bool
		nilAtom    Atom
	)

	rest = trimTagSpaceLeft(rest)
	if strings.HasPrefix(rest, "=>") {
		rest = trimTagSpaceLeft(rest[2:])
		valStr, afterVal, err := scanValueType(rest)
		if err != nil {
			return fail(err.Error())
		}
		spec, err := parseTypeSpec(valStr)
		if err != nil {
			return fail(err.Error())
		}
		valSpec = spec
		rest = trimTagSpaceLeft(afterVal)
	}

	if rest != "" {
		if rest[0] != ',' {
			return fail(fmt.Sprintf("unexpected token %q in tag", rest))
		}
		for _, rawOpt := range strings.Split(rest[1:], ",") {
			opt := strings.Trim(rawOpt, " \t")
			if opt == "" {
				return fail("empty tag option")
			}
			switch {
			case opt == "required":
				if required {
					return fail("duplicate required option")
				}
				required = true
			case strings.HasPrefix(opt, "null="):
				if hasNilAtom {
					return fail("duplicate null= option")
				}
				atomStr := opt[len("null="):]
				if err := validateAtom(atomStr); err != nil {
					return fail(fmt.Sprintf("invalid null= atom: %v", err))
				}
				hasNilAtom = true
				nilAtom = Atom(atomStr)
			default:
				return fail(fmt.Sprintf("unknown tag option %q", opt))
			}
		}
	}

	pmk, err := buildParsedMapKey(keyName, keyType)
	if err != nil {
		return fail(err.Error())
	}

	if err := validateFieldTypeForSpec(sf.Type, valSpec); err != nil {
		return fail(err.Error())
	}

	var kw wireWriter
	if err := kw.writeParsedMapKey(pmk); err != nil {
		return fail(err.Error())
	}

	return fieldInfo{
		name:       sf.Name,
		index:      index,
		typ:        sf.Type,
		tag:        tag,
		key:        pmk,
		keyWire:    kw.bytes(),
		valSpec:    valSpec,
		required:   required,
		hasNilAtom: hasNilAtom,
		nilAtom:    nilAtom,
	}, nil
}

func trimTagSpaceLeft(s string) string {
	return strings.TrimLeft(s, " \t")
}

// splitTagKeyPart splits the leading "<name>:<key_type>" segment up to the first ',', '=>', or end of string.
func splitTagKeyPart(s string) (keyPart string, rest string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' || (s[i] == '=' && i+1 < len(s) && s[i+1] == '>') {
			return strings.TrimRight(s[:i], " \t"), s[i:]
		}
	}
	return strings.TrimRight(s, " \t"), ""
}

func validateTagKeyName(name string) error {
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("key name %q cannot contain whitespace or control characters", name)
		}
		switch r {
		case '(', ')', '\'', '"', ',', '\\':
			return fmt.Errorf("invalid character %q in key name %q", r, name)
		}
	}
	return nil
}

// scanValueType scans a value type expression (such as `integer` or `list(list(integer))`)
// up to the first top-level comma or end of string.
func scanValueType(s string) (valStr string, rest string, err error) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return "", "", fmt.Errorf("unbalanced ')' in value type")
			}
		case ',':
			if depth == 0 {
				return strings.TrimRight(s[:i], " \t"), s[i:], nil
			}
		}
	}
	if depth != 0 {
		return "", "", fmt.Errorf("unbalanced '(' in value type")
	}
	return strings.TrimRight(s, " \t"), "", nil
}

func parseKeyType(s string) (KeyType, error) {
	switch s {
	case "atom":
		return KeyAtom, nil
	case "binary":
		return KeyBinary, nil
	case "charlist":
		return KeyCharlist, nil
	case "integer":
		return KeyInteger, nil
	case "float":
		return KeyFloat, nil
	case "boolean":
		return KeyBoolean, nil
	default:
		return 0, fmt.Errorf("unsupported key type %q (allowed: atom, binary, charlist, integer, float, boolean)", s)
	}
}

func buildParsedMapKey(name string, kt KeyType) (parsedMapKey, error) {
	pmk := parsedMapKey{
		keyType: kt,
		rawName: name,
	}
	switch kt {
	case KeyAtom:
		if err := validateAtom(name); err != nil {
			return parsedMapKey{}, fmt.Errorf("invalid atom key %q: %w", name, err)
		}
		pmk.canon = structKey{kind: keyKindAtom, str: name}
	case KeyBinary:
		pmk.canon = structKey{kind: keyKindBinary, str: name}
	case KeyCharlist:
		if !utf8.ValidString(name) {
			return parsedMapKey{}, fmt.Errorf("charlist key %q is not valid UTF-8", name)
		}
		pmk.canon = structKey{kind: keyKindCharlist, str: name}
	case KeyInteger:
		bi, ok := new(big.Int).SetString(name, 10)
		if !ok {
			return parsedMapKey{}, fmt.Errorf("integer key %q is not a valid base-10 integer", name)
		}
		pmk.intVal = bi
		pmk.canon = structKey{kind: keyKindInteger, str: bi.String()}
	case KeyFloat:
		f, err := strconv.ParseFloat(name, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return parsedMapKey{}, fmt.Errorf("float key %q is not a valid finite float", name)
		}
		pmk.floatVal = f
		pmk.canon = structKey{kind: keyKindFloat, floatBits: math.Float64bits(f)}
	case KeyBoolean:
		if name != "true" && name != "false" {
			return parsedMapKey{}, fmt.Errorf("boolean key %q must be \"true\" or \"false\"", name)
		}
		pmk.boolVal = name == "true"
		// In Erlang, booleans are atoms on the wire, so true/false share key identity with atom true/false.
		pmk.canon = structKey{kind: keyKindAtom, str: name}
	default:
		return parsedMapKey{}, fmt.Errorf("unknown key type %v", kt)
	}
	return pmk, nil
}

// parseTypeSpec parses an Erlang value type expression such as:
// integer, float, number, atom, boolean, binary, bitstring, charlist,
// list, list(atom), list(list(integer)), map.
func parseTypeSpec(s string) (*typeSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty value type")
	}

	switch s {
	case "integer":
		return &typeSpec{kind: valInteger, rawText: s}, nil
	case "float":
		return &typeSpec{kind: valFloat, rawText: s}, nil
	case "number":
		return &typeSpec{kind: valNumber, rawText: s}, nil
	case "atom":
		return &typeSpec{kind: valAtom, rawText: s}, nil
	case "boolean":
		return &typeSpec{kind: valBoolean, rawText: s}, nil
	case "binary":
		return &typeSpec{kind: valBinary, rawText: s}, nil
	case "bitstring":
		return &typeSpec{kind: valBitstring, rawText: s}, nil
	case "charlist":
		return &typeSpec{kind: valCharlist, rawText: s}, nil
	case "list":
		return &typeSpec{kind: valList, rawText: s}, nil
	case "map":
		return &typeSpec{kind: valMap, rawText: s}, nil
	case "tuple":
		return &typeSpec{kind: valTuple, rawText: s}, nil
	case "datetime":
		return &typeSpec{kind: valDateTime, rawText: s}, nil
	}

	if strings.HasPrefix(s, "list(") && strings.HasSuffix(s, ")") {
		inner := s[len("list(") : len(s)-1]
		elemSpec, err := parseTypeSpec(inner)
		if err != nil {
			return nil, fmt.Errorf("invalid list element type in %q: %w", s, err)
		}
		return &typeSpec{kind: valList, elem: elemSpec, rawText: s}, nil
	}

	return nil, fmt.Errorf("unknown tag option or value type %q", s)
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func implementsTupleCodec(base reflect.Type) bool {
	if base.Kind() == reflect.Interface {
		return false
	}
	hasMarshaler := base.Implements(marshalerType) || reflect.PointerTo(base).Implements(marshalerType)
	hasUnmarshaler := base.Implements(unmarshalerType) || reflect.PointerTo(base).Implements(unmarshalerType)
	return hasMarshaler && hasUnmarshaler
}

func specContainsTupleOrDateTime(spec *typeSpec) bool {
	for spec != nil {
		if spec.kind == valTuple || spec.kind == valDateTime {
			return true
		}
		spec = spec.elem
	}
	return false
}

func validateFieldTypeForSpec(ft reflect.Type, spec *typeSpec) error {
	if spec == nil {
		return nil
	}
	base := derefType(ft)
	switch spec.kind {
	case valTuple:
		if !implementsTupleCodec(base) {
			return fmt.Errorf("tuple field type %s must implement etf.Marshaler and etf.Unmarshaler", ft)
		}
		return nil
	case valDateTime:
		if !implementsTupleCodec(base) {
			return fmt.Errorf("datetime field type %s must implement etf.Marshaler and etf.Unmarshaler (e.g. etf.DateTime)", ft)
		}
		return nil
	case valList:
		if spec.elem == nil {
			return nil
		}
		if implementsTupleCodec(base) {
			return nil
		}
		if base.Kind() == reflect.Slice || base.Kind() == reflect.Array {
			return validateFieldTypeForSpec(base.Elem(), spec.elem)
		}
		if specContainsTupleOrDateTime(spec.elem) {
			return fmt.Errorf("field type %s cannot hold %s elements without implementing etf.Marshaler and etf.Unmarshaler", ft, spec.rawText)
		}
		return nil
	}
	return nil
}
