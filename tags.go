package etf

// Erlang External Term Format (ETF) wire tags.
// Reference: https://www.erlang.org/doc/apps/erts/erl_ext_dist.html
const (
	VersionMagic byte = 131 // External term format version magic byte

	TagCompressed    byte = 80  // Zlib compressed term payload
	TagNewFloat      byte = 70  // 8-byte IEEE-754 big-endian finite float64
	TagBitBinary     byte = 77  // Bitstring (non-byte-aligned binary)
	TagSmallInt      byte = 97  // Unsigned 8-bit integer (0..255)
	TagInt           byte = 98  // Signed 32-bit big-endian integer
	TagSmallTuple    byte = 104 // Tuple with 1-byte arity
	TagLargeTuple    byte = 105 // Tuple with 4-byte arity
	TagNil           byte = 106 // Empty list []
	TagString        byte = 107 // Byte-list optimization (2-byte length, bytes 0..255)
	TagList          byte = 108 // General list (4-byte length + elements + tail)
	TagBinary        byte = 109 // Binary (4-byte length + raw bytes)
	TagSmallBig      byte = 110 // Arbitrary-precision integer (1-byte length)
	TagLargeBig      byte = 111 // Arbitrary-precision integer (4-byte length)
	TagMap           byte = 116 // Map (4-byte arity + key/value pairs)
	TagAtomUTF8      byte = 118 // UTF-8 atom (2-byte byte length, <= 255 runes)
	TagSmallAtomUTF8 byte = 119 // UTF-8 atom (1-byte byte length, <= 255 runes)
)

type tagKind uint8

const (
	tagUnknown tagKind = iota
	tagInteger
	tagFloat
	tagAtom
	tagBinary
	tagBitstring
	tagNil
	tagString
	tagList
	tagTuple
	tagMap
)

var tagTable = [256]tagKind{
	TagSmallInt:      tagInteger,
	TagInt:           tagInteger,
	TagSmallBig:      tagInteger,
	TagLargeBig:      tagInteger,
	TagNewFloat:      tagFloat,
	TagSmallAtomUTF8: tagAtom,
	TagAtomUTF8:      tagAtom,
	TagBinary:        tagBinary,
	TagBitBinary:     tagBitstring,
	TagNil:           tagNil,
	TagString:        tagString,
	TagList:          tagList,
	TagSmallTuple:    tagTuple,
	TagLargeTuple:    tagTuple,
	TagMap:           tagMap,
}
