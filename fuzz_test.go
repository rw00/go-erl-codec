package etf

import "testing"

func FuzzUnmarshal(f *testing.F) {
	seeds := [][]byte{
		{VersionMagic, TagNil},
		{VersionMagic, TagSmallInt, 42},
		{VersionMagic, TagInt, 0xFF, 0xFF, 0xFF, 0xFF},
		{VersionMagic, TagSmallAtomUTF8, 4, 't', 'r', 'u', 'e'},
		{VersionMagic, TagBinary, 0, 0, 0, 3, 'a', 'b', 'c'},
		{VersionMagic, TagString, 0, 3, 1, 2, 3},
		{VersionMagic, TagMap, 0, 0, 0, 1, TagSmallAtomUTF8, 1, 'k', TagSmallInt, 1},
		{VersionMagic, TagSmallBig, 2, 1, 0xFF, 0x01},
		{VersionMagic, TagBitBinary, 0, 0, 0, 1, 3, 0xE0},
		{VersionMagic, TagSmallTuple, 2, TagSmallAtomUTF8, 2, 'o', 'k', TagSmallInt, 42},
		{VersionMagic, TagLargeTuple, 0, 0, 0, 1, TagSmallInt, 7},
		{
			VersionMagic, TagSmallTuple, 2,
			TagSmallTuple, 3, TagInt, 0, 0, 0x07, 0xEA, TagSmallInt, 11, TagSmallInt, 24,
			TagSmallTuple, 3, TagSmallInt, 19, TagSmallInt, 50, TagSmallInt, 41,
		},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var v any
		err := Unmarshal(data, &v, WithMaxDecodeDepth(64), WithMaxUncompressedSize(1<<20))
		if err != nil {
			return
		}
		// Any validly decoded term must re-encode cleanly without panicking!
		encoded, err := Marshal(v)
		if err != nil {
			t.Fatalf("Marshal of decoded fuzz term failed: %v (term=%#v)", err, v)
		}
		var v2 any
		if err := Unmarshal(encoded, &v2, WithMaxDecodeDepth(64), WithMaxUncompressedSize(1<<20)); err != nil {
			t.Fatalf("re-Unmarshal failed: %v", err)
		}
		if !Equal(v, v2) {
			t.Fatalf("fuzz round-trip mismatch:\nv1=%#v\nv2=%#v", v, v2)
		}
	})
}
