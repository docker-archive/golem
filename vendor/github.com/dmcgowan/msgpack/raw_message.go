package msgpack

import (
	"bytes"
	"fmt"
	"io"
)

type RawMessage struct {
	raw []byte
	m   *structCache
	mf  func(*Decoder) (interface{}, error)
}

func (r RawMessage) Decode(v ...interface{}) error {
	w := bytes.NewReader(r.raw)
	decoder := NewDecoder(w)
	decoder.m = r.m
	decoder.DecodeMapFunc = r.mf
	return decoder.Decode(v...)
}

func (r *RawMessage) copyToWriter(w writer, m *structCache) error {
	decoder := NewDecoder(bytes.NewReader(r.raw))
	decoder.m = r.m
	decoder.DecodeMapFunc = r.mf

	// Create extension copier
	extCopy := func(b byte, aw writer) error {
		if err := decoder.r.UnreadByte(); err != nil {
			return err
		}

		v, err := decoder.DecodeExtended()
		if err != nil {
			return err
		}

		encoder := NewEncoder(aw)
		encoder.m = m

		return encoder.Encode(v)
	}

	return decoder.copyIntoBuffer(w, extCopy)

}

func (d *Decoder) DecodeRawMessage() (RawMessage, error) {
	w := bytes.NewBuffer(nil)

	if err := d.copyIntoBuffer(w, d.rawExtCopy); err != nil {
		if err == io.EOF {
			return RawMessage{}, err
		}
		return RawMessage{}, fmt.Errorf("Error copying: %s, with content %#v", err, w.Bytes())
	}

	return RawMessage{raw: w.Bytes(), m: d.m, mf: d.DecodeMapFunc}, nil
}

func iterN(n int) []struct{} {
	return make([]struct{}, n)
}

func (d *Decoder) copyNBytes(w writer, n int) error {
	b, err := d.readN(n)
	if err != nil {
		if err == io.EOF {
			return err
		}
		return fmt.Errorf("Error reading %d bytes: %s", n, err)
	}
	_, err = w.Write(b)
	if err != nil {
		if err == io.EOF {
			return err
		}
		return fmt.Errorf("Error copying %d bytes: %s", n, err)
	}
	return nil
}

func (d *Decoder) copyLen8(w writer) (int, error) {
	b, err := d.r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, err
		}
		return 0, fmt.Errorf("Error copying len(16): %s", err)
	}
	return int(b), w.WriteByte(b)
}

func (d *Decoder) copyLen16(w writer) (int, error) {
	b, err := d.readN(2)
	if err != nil {
		if err == io.EOF {
			return 0, err
		}
		return 0, fmt.Errorf("Error copying len(16): %s", err)
	}
	_, err = w.Write(b)
	return int((uint16(b[0]) << 8) | uint16(b[1])), err
}

func (d *Decoder) copyLen32(w writer) (int, error) {
	b, err := d.readN(4)
	if err != nil {
		if err == io.EOF {
			return 0, err
		}
		return 0, fmt.Errorf("Error copying len(32): %s", err)
	}
	_, err = w.Write(b)
	n := int((uint32(b[0]) << 24) |
		(uint32(b[1]) << 16) |
		(uint32(b[2]) << 8) |
		uint32(b[3]))
	return n, err
}

func (d *Decoder) rawExtCopy(b byte, w writer) error {
	if err := w.WriteByte(b); err != nil {
		return fmt.Errorf("Error writing first byte: %s", err)
	}
	switch b {
	case ext8Code:
		nb, err := d.copyLen8(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, nb+1)
	case ext16Code:
		nb, err := d.copyLen16(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, nb+1)
	case ext32Code:
		nb, err := d.copyLen32(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, nb+1)
	case fixExt1Code:
		return d.copyNBytes(w, 2)
	case fixExt2Code:
		return d.copyNBytes(w, 3)
	case fixExt4Code:
		return d.copyNBytes(w, 5)
	case fixExt8Code:
		return d.copyNBytes(w, 9)
	case fixExt16Code:
		return d.copyNBytes(w, 17)
	default:
		return fmt.Errorf("non extension type: %x", b)
	}
}

func (d *Decoder) copyIntoBuffer(w writer, extCopy func(byte, writer) error) error {
	b, err := d.r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return err
		}
		return fmt.Errorf("Error reading first byte: %s", err)
	}
	if (b >= ext8Code && b <= ext32Code) || (b >= fixExt1Code && b <= fixExt16Code) {
		return extCopy(b, w)
	}
	if err := w.WriteByte(b); err != nil {
		return fmt.Errorf("Error writing first byte: %s", err)
	}

	if b <= posFixNumHighCode || b >= negFixNumLowCode {
		return nil
	}
	if b <= fixMapHighCode {
		for _ = range iterN(int(b&fixMapMask) * 2) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying map object: %s", err)
			}
		}
		return nil
	}
	if b <= fixArrayHighCode {
		for _ = range iterN(int(b & fixArrayMask)) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying array value: %s", err)
			}
		}
		return nil
	}
	if b <= fixStrHighCode {
		return d.copyNBytes(w, int(b&fixStrMask))
	}

	switch b {
	case uint8Code, int8Code:
		nb, err := d.r.ReadByte()
		if err != nil {
			return err
		}
		return w.WriteByte(nb)
	case uint16Code, int16Code:
		return d.copyNBytes(w, 2)
	case uint32Code, int32Code, floatCode:
		return d.copyNBytes(w, 4)
	case uint64Code, int64Code, doubleCode:
		return d.copyNBytes(w, 8)
	case bin8Code, str8Code:
		l, err := d.copyLen8(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, int(l))
	case bin16Code, str16Code:
		l, err := d.copyLen16(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, l)
	case bin32Code, str32Code:
		l, err := d.copyLen32(w)
		if err != nil {
			return err
		}
		return d.copyNBytes(w, l)
	case array16Code:
		l, err := d.copyLen16(w)
		if err != nil {
			return err
		}
		for _ = range iterN(l) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying array value: %s", err)
			}
		}
	case array32Code:
		l, err := d.copyLen32(w)
		if err != nil {
			return err
		}
		for _ = range iterN(l) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying array value: %s", err)
			}
		}
	case map16Code:
		l, err := d.copyLen16(w)
		if err != nil {
			return err
		}
		for _ = range iterN(l * 2) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying map object: %s", err)
			}
		}
	case map32Code:
		l, err := d.copyLen32(w)
		if err != nil {
			return err
		}
		for _ = range iterN(l * 2) {
			if err := d.copyIntoBuffer(w, extCopy); err != nil {
				if err == io.EOF {
					return err
				}
				return fmt.Errorf("Error copying map object: %s", err)
			}
		}

	default:
	}

	return nil
}

func (e *Encoder) encodeRawMessage(r *RawMessage) error {
	return r.copyToWriter(e.w, e.m)
}

func DecodeMapToRaw(d *Decoder) (interface{}, error) {
	return d.DecodeRawMessage()
}
