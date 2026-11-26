package apnsrelay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

var (
	errInvalidCBOR = errors.New("invalid CBOR encoding")
)

const (
	cborMaxDepth = 16
	cborMaxItems = 1024
)

// decodeCBOR strictly decodes a single definite-length RFC 8949 CBOR value
// (supporting unsigned/negative integers, byte strings, text strings, arrays,
// maps, booleans, and null) and rejects trailing bytes or indefinite lengths.
func decodeCBOR(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, errInvalidCBOR
	}
	d := cborDecoder{data: data}
	v, err := d.readValue(0)
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.data) {
		return nil, fmt.Errorf("%w: trailing bytes", errInvalidCBOR)
	}
	return v, nil
}

type cborDecoder struct {
	data []byte
	pos  int
}

func (d *cborDecoder) readByte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, errInvalidCBOR
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

func (d *cborDecoder) readBytes(n uint64) ([]byte, error) {
	if n > uint64(len(d.data)-d.pos) {
		return nil, errInvalidCBOR
	}
	out := make([]byte, int(n))
	copy(out, d.data[d.pos:d.pos+int(n)])
	d.pos += int(n)
	return out, nil
}

func (d *cborDecoder) readUint(ai byte) (uint64, error) {
	switch {
	case ai < 24:
		return uint64(ai), nil
	case ai == 24:
		b, err := d.readByte()
		return uint64(b), err
	case ai == 25:
		raw, err := d.readBytes(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(raw)), nil
	case ai == 26:
		raw, err := d.readBytes(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(raw)), nil
	case ai == 27:
		raw, err := d.readBytes(8)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(raw), nil
	default:
		// 28-30 reserved, 31 indefinite length (rejected)
		return 0, errInvalidCBOR
	}
}

func (d *cborDecoder) readValue(depth int) (any, error) {
	if depth > cborMaxDepth {
		return nil, fmt.Errorf("%w: max nesting exceeded", errInvalidCBOR)
	}
	ib, err := d.readByte()
	if err != nil {
		return nil, err
	}
	major := ib >> 5
	ai := ib & 0x1f

	switch major {
	case 0: // unsigned integer
		return d.readUint(ai)
	case 1: // negative integer (-1 - n)
		n, err := d.readUint(ai)
		if err != nil {
			return nil, err
		}
		if n > math.MaxInt64 {
			return nil, errInvalidCBOR
		}
		return -1 - int64(n), nil
	case 2: // byte string
		n, err := d.readUint(ai)
		if err != nil {
			return nil, err
		}
		return d.readBytes(n)
	case 3: // UTF-8 text string
		n, err := d.readUint(ai)
		if err != nil {
			return nil, err
		}
		raw, err := d.readBytes(n)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, errInvalidCBOR
		}
		return string(raw), nil
	case 4: // array
		n, err := d.readUint(ai)
		if err != nil || n > cborMaxItems {
			return nil, errInvalidCBOR
		}
		arr := make([]any, 0, int(n))
		for i := uint64(0); i < n; i++ {
			item, err := d.readValue(depth + 1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, item)
		}
		return arr, nil
	case 5: // map (string or int keys; App Attest uses text keys)
		n, err := d.readUint(ai)
		if err != nil || n > cborMaxItems {
			return nil, errInvalidCBOR
		}
		m := make(map[string]any, int(n))
		for i := uint64(0); i < n; i++ {
			k, err := d.readValue(depth + 1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("%w: non-string map key", errInvalidCBOR)
			}
			if _, dup := m[ks]; dup {
				return nil, fmt.Errorf("%w: duplicate map key", errInvalidCBOR)
			}
			v, err := d.readValue(depth + 1)
			if err != nil {
				return nil, err
			}
			m[ks] = v
		}
		return m, nil
	case 7: // simple values
		switch ai {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22, 23:
			return nil, nil
		default:
			return nil, errInvalidCBOR
		}
	default:
		// Major type 6 (tags) is not used in Apple App Attest objects.
		return nil, errInvalidCBOR
	}
}

// encodeCBOR encodes supported Go values (maps with string keys, slices,
// strings, []byte, uint64, int, bool) into definite-length CBOR. Used by tests.
func encodeCBOR(v any) ([]byte, error) {
	var out []byte
	if err := appendCBOR(&out, v); err != nil {
		return nil, err
	}
	return out, nil
}

func appendCBORHeader(out *[]byte, major byte, n uint64) {
	prefix := major << 5
	switch {
	case n < 24:
		*out = append(*out, prefix|byte(n))
	case n <= 0xff:
		*out = append(*out, prefix|24, byte(n))
	case n <= 0xffff:
		*out = append(*out, prefix|25, byte(n>>8), byte(n))
	case n <= 0xffffffff:
		*out = append(*out, prefix|26, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	default:
		*out = append(*out, prefix|27,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

func appendCBOR(out *[]byte, v any) error {
	switch x := v.(type) {
	case uint64:
		appendCBORHeader(out, 0, x)
	case int:
		if x >= 0 {
			appendCBORHeader(out, 0, uint64(x))
		} else {
			appendCBORHeader(out, 1, uint64(-1-x))
		}
	case []byte:
		appendCBORHeader(out, 2, uint64(len(x)))
		*out = append(*out, x...)
	case string:
		appendCBORHeader(out, 3, uint64(len(x)))
		*out = append(*out, x...)
	case []any:
		appendCBORHeader(out, 4, uint64(len(x)))
		for _, item := range x {
			if err := appendCBOR(out, item); err != nil {
				return err
			}
		}
	case [][]byte:
		appendCBORHeader(out, 4, uint64(len(x)))
		for _, item := range x {
			if err := appendCBOR(out, item); err != nil {
				return err
			}
		}
	case map[string]any:
		appendCBORHeader(out, 5, uint64(len(x)))
		for k, val := range x {
			if err := appendCBOR(out, k); err != nil {
				return err
			}
			if err := appendCBOR(out, val); err != nil {
				return err
			}
		}
	case bool:
		if x {
			*out = append(*out, (7<<5)|21)
		} else {
			*out = append(*out, (7<<5)|20)
		}
	default:
		return fmt.Errorf("unsupported CBOR encode type %T", v)
	}
	return nil
}
