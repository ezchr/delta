package packconv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
)

// decodeTGA decodes a Truevision TGA image: true-colour (24/32 bit) and greyscale, raw or
// run-length encoded, which is what Bedrock packs use.
func decodeTGA(b []byte) (image.Image, error) {
	if len(b) < 18 {
		return nil, errors.New("tga: short header")
	}
	idLen := int(b[0])
	cmapType, imgType := b[1], b[2]
	cmapLen := int(binary.LittleEndian.Uint16(b[5:7]))
	cmapBits := int(b[7])
	w := int(binary.LittleEndian.Uint16(b[12:14]))
	h := int(binary.LittleEndian.Uint16(b[14:16]))
	bpp := int(b[16])
	desc := b[17]
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, errors.New("tga: bad size")
	}
	rle := imgType >= 9
	base := imgType &^ 8
	if base != 2 && base != 3 {
		return nil, errors.New("tga: unsupported type")
	}
	if bpp != 8 && bpp != 24 && bpp != 32 && !(base == 3 && bpp == 16) {
		return nil, errors.New("tga: unsupported depth")
	}
	off := 18 + idLen
	if cmapType == 1 {
		off += cmapLen * ((cmapBits + 7) / 8)
	}
	px := bpp / 8
	n := w * h
	raw := make([]byte, 0, n*px)
	data := b[min(off, len(b)):]
	if rle {
		for i := 0; len(raw) < n*px && i < len(data); {
			hd := data[i]
			i++
			cnt := int(hd&0x7f) + 1
			if hd&0x80 != 0 {
				if i+px > len(data) {
					break
				}
				for k := 0; k < cnt; k++ {
					raw = append(raw, data[i:i+px]...)
				}
				i += px
			} else {
				end := min(i+cnt*px, len(data))
				raw = append(raw, data[i:end]...)
				i = end
			}
		}
	} else {
		raw = append(raw, data[:min(len(data), n*px)]...)
	}
	if len(raw) < n*px {
		raw = append(raw, make([]byte, n*px-len(raw))...)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	topDown := desc&0x20 != 0
	rightLeft := desc&0x10 != 0
	for i := 0; i < n; i++ {
		x, y := i%w, i/w
		if !topDown {
			y = h - 1 - y
		}
		if rightLeft {
			x = w - 1 - x
		}
		p := raw[i*px:]
		var c color.NRGBA
		switch {
		case base == 3 && px == 1:
			c = color.NRGBA{p[0], p[0], p[0], 255}
		case base == 3 && px == 2:
			c = color.NRGBA{p[0], p[0], p[0], p[1]}
		case px == 3:
			c = color.NRGBA{p[2], p[1], p[0], 255}
		case px == 4:
			c = color.NRGBA{p[2], p[1], p[0], p[3]}
		}
		img.SetNRGBA(x, y, c)
	}
	return img, nil
}

// toPNG returns a texture file as PNG data: PNGs as they are, TGAs converted.
func toPNG(b []byte, tga bool) ([]byte, image.Point, error) {
	if !tga {
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return nil, image.Point{}, err
		}
		return b, image.Pt(cfg.Width, cfg.Height), nil
	}
	img, err := decodeTGA(b)
	if err != nil {
		return nil, image.Point{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, image.Point{}, err
	}
	return buf.Bytes(), img.Bounds().Size(), nil
}
