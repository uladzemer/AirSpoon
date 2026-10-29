package airplay

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"howett.net/plist"
)

func overflowingNetworkPlist(tag byte) []byte {
	// One object whose extended length wraps start+length to before start.
	data := append([]byte("bplist00"), tag, 0x13)
	data = append(data, bytes.Repeat([]byte{0xff}, 8)...)
	offsetTable := len(data)
	data = append(data, 8)
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 1, 1
	binary.BigEndian.PutUint64(trailer[8:], 1)
	binary.BigEndian.PutUint64(trailer[24:], uint64(offsetTable))
	return append(data, trailer...)
}

func TestNetworkPlistRecoversLengthOverflow(t *testing.T) {
	for _, tag := range []byte{0x4f, 0x5f, 0x6f, 0xaf, 0xdf} {
		var decoded interface{}
		if _, err := decodeNetworkPlist(overflowingNetworkPlist(tag), &decoded); err == nil {
			t.Fatalf("overflowing tag 0x%x accepted", tag)
		}
	}
	var decoded interface{}
	if _, err := decodeNetworkPlist(make([]byte, maxResponseBody+1), &decoded); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized plist: %v", err)
	}
	for _, format := range []int{plist.BinaryFormat, plist.XMLFormat} {
		data, err := plist.Marshal(map[string]string{"name": "TV"}, format)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]string
		if _, err := decodeNetworkPlist(data, &value); err != nil || value["name"] != "TV" {
			t.Fatalf("normal plist: %v, %v", value, err)
		}
	}
}

type panickingPlistValue struct{}

func (*panickingPlistValue) UnmarshalPlist(func(interface{}) error) error {
	// go-plist deliberately re-panics runtime.Error; check our outer boundary.
	var empty []byte
	_ = empty[0]
	return nil
}

func TestNetworkPlistRecoversDecoderRuntimePanic(t *testing.T) {
	data, err := plist.Marshal(map[string]string{"name": "TV"}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	var value panickingPlistValue
	if _, err := decodeNetworkPlist(data, &value); err == nil || !strings.Contains(err.Error(), "decode network plist") {
		t.Fatalf("decoder runtime panic was not recovered: %v", err)
	}
}
