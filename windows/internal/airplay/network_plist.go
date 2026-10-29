package airplay

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"howett.net/plist"
)

// decodeNetworkPlist bounds untrusted input and contains runtime panics from
// go-plist's binary length arithmetic (including uint64 overflow in v1.0.1).
func decodeNetworkPlist(data []byte, value interface{}) (format int, err error) {
	if len(data) > maxResponseBody {
		return 0, fmt.Errorf("network plist exceeds %d byte limit", maxResponseBody)
	}
	if err := validateBinaryPlistLengths(data); err != nil {
		return 0, err
	}
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("decode network plist: %v", failure)
		}
	}()
	return plist.Unmarshal(data, value)
}

// Preflight extended lengths before go-plist does unchecked uint64 arithmetic
// or creates unsafe ASCII strings. Recovery alone cannot contain such strings.
func validateBinaryPlistLengths(data []byte) error {
	if !bytes.HasPrefix(data, []byte("bplist")) {
		return nil
	}
	invalid := func() error { return fmt.Errorf("invalid binary plist length or offset") }
	if len(data) < 40 {
		return invalid()
	}
	trailer := data[len(data)-32:]
	offsetSize := uint64(trailer[6])
	refSize := uint64(trailer[7])
	count := binary.BigEndian.Uint64(trailer[8:])
	table := binary.BigEndian.Uint64(trailer[24:])
	end := uint64(len(data) - 32)
	if offsetSize == 0 || offsetSize > 8 || refSize == 0 || refSize > 8 ||
		table < 8 || table > end || count > (end-table)/offsetSize {
		return invalid()
	}
	for index := uint64(0); index < count; index++ {
		var offset uint64
		for _, octet := range data[table+index*offsetSize : table+(index+1)*offsetSize] {
			offset = offset<<8 | uint64(octet)
		}
		if offset < 8 || offset >= table {
			return invalid()
		}
		tag := data[offset]
		switch tag & 0xf0 {
		case 0x40, 0x50, 0x60, 0xa0, 0xd0:
			if tag&0xf != 0xf {
				continue
			}
		default:
			continue
		}
		if offset+2 > table || data[offset+1]&0xf0 != 0x10 || data[offset+1]&0xf > 4 {
			return invalid()
		}
		size := uint64(1) << (data[offset+1] & 0xf)
		if size > table-(offset+2) {
			return invalid()
		}
		var length uint64
		for _, octet := range data[offset+2 : offset+2+size] {
			if length > uint64(len(data)) {
				return invalid()
			}
			length = length<<8 | uint64(octet)
		}
		if length > uint64(len(data)) {
			return invalid()
		}
	}
	return nil
}
