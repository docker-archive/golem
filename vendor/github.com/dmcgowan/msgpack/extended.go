package msgpack

import (
	"errors"
	"reflect"
)

type EncodeExtFunc func(reflect.Value) (int, []byte, error)
type DecodeExtFunc func(reflect.Value, []byte) error

type decodeExtInfo struct {
	DecodeType    reflect.Type
	DecodeHandler DecodeExtFunc
}

type Extensions struct {
	extensions map[int]*decodeExtInfo
	encodeFunc EncodeExtFunc
	decTypeMap map[reflect.Type]decoderFunc
}

func NewExtensions() *Extensions {
	return &Extensions{
		extensions: make(map[int]*decodeExtInfo),
		decTypeMap: make(map[reflect.Type]decoderFunc),
	}
}

func (ext *Extensions) getEncoder(typeEncoder encoderFunc) encoderFunc {
	if ext.encodeFunc != nil {
		return func(e *Encoder, iv reflect.Value) error {
			code, b, err := ext.encodeFunc(iv)
			if err != nil {
				return err
			}
			if b != nil {
				return e.EncodeExtended(code, b)
			}
			return typeEncoder(e, iv)
		}
	}
	return typeEncoder
}

func (ext *Extensions) SetEncoder(encoder EncodeExtFunc) {
	ext.encodeFunc = encoder
}

func (ext *Extensions) AddDecoder(code int, decType reflect.Type, decode DecodeExtFunc) {
	ext.extensions[code] = &decodeExtInfo{
		DecodeType:    decType,
		DecodeHandler: decode,
	}
	ext.decTypeMap[decType] = func(d *Decoder, v reflect.Value) error {
		c, b, err := d.DecodeExtendedBytes()
		if err != nil {
			return err
		}
		if c != code {
			return errors.New("unexpected extended code")
		}

		return decode(v, b)
	}
}

func (d *Decoder) DecodeExtendedBytes() (int, []byte, error) {
	c, err := d.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}

	var l int
	switch c {
	case fixExt1Code:
		l = 1
	case fixExt2Code:
		l = 2
	case fixExt4Code:
		l = 4
	case fixExt8Code:
		l = 8
	case fixExt16Code:
		l = 16
	case ext8Code:
		v, err := d.uint8()
		if err != nil {
			return 0, nil, err
		}
		l = int(v)

	case ext16Code:
		v, err := d.uint16()
		if err != nil {
			return 0, nil, err
		}
		l = int(v)
	case ext32Code:
		v, err := d.uint32()
		if err != nil {
			return 0, nil, err
		}
		l = int(v)
	default:
		return 0, nil, errors.New("unexpected code")
	}
	typ, err := d.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}

	b, err := d.readN(l)

	return int(typ), b, err
}

func (d *Decoder) DecodeExtended() (interface{}, error) {
	typ, b, err := d.DecodeExtendedBytes()
	if err != nil {
		return nil, err
	}
	if d.m.ext == nil {
		return nil, errors.New("no extended types")
	}

	ext := d.m.ext.extensions[int(typ)]
	if ext == nil {
		return nil, errors.New("extended type not registered")
	}

	v := reflect.New(ext.DecodeType).Elem()

	if err := ext.DecodeHandler(v, b); err != nil {
		return nil, err
	}

	return v.Interface(), nil
}

func (e *Encoder) EncodeExtended(ext int, data []byte) error {
	switch l := len(data); {
	case l == 1:
		if err := e.w.WriteByte(fixExt1Code); err != nil {
			return err
		}
	case l == 2:
		if err := e.w.WriteByte(fixExt2Code); err != nil {
			return err
		}
	case l == 4:
		if err := e.w.WriteByte(fixExt4Code); err != nil {
			return err
		}
	case l == 8:
		if err := e.w.WriteByte(fixExt8Code); err != nil {
			return err
		}
	case l == 16:
		if err := e.w.WriteByte(fixExt16Code); err != nil {
			return err
		}
	case l < 256:
		if err := e.write([]byte{
			ext8Code,
			byte(l),
		}); err != nil {
			return err
		}
	case l < 65536:
		if err := e.write([]byte{
			ext16Code,
			byte(l >> 8),
			byte(l),
		}); err != nil {
			return err
		}
	default:
		if err := e.write([]byte{
			ext32Code,
			byte(l >> 24),
			byte(l >> 16),
			byte(l >> 8),
			byte(l),
		}); err != nil {
			return err
		}
	}
	if err := e.w.WriteByte(byte(ext)); err != nil {
		return err
	}
	return e.write(data)
}

func (m *structCache) addExtensions(ext *Extensions) {
	m.l.Lock()
	defer m.l.Unlock()
	m.ext = ext
}

func (e *Encoder) AddExtensions(ext *Extensions) {
	e.m.addExtensions(ext)
}

func (d *Decoder) AddExtensions(ext *Extensions) {
	d.m.addExtensions(ext)
}
