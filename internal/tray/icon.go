package tray

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// Pixmap es un ícono ARGB32 en orden de red, como lo pide la especificación.
type Pixmap struct {
	W, H int32
	Data []byte
}

// Render escala el ícono a size×size y, si badge, le agrega el punto rojo de "sin leer"
// arriba a la derecha (con un borde transparente para que se distinga sobre cualquier fondo).
func Render(src image.Image, size int, badge bool) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	if !badge {
		return dst
	}
	r := float64(size) * 0.22
	if r < 3.5 {
		r = 3.5
	}
	cx, cy := float64(size)-r-0.5, r+0.5
	ring := math.Max(1, float64(size)/16)
	red := color.NRGBA{0xF2, 0x3F, 0x43, 0xFF}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			// Borde: se recorta el ícono alrededor del punto.
			if cut := clamp01(r + ring + 0.5 - d); cut > 0 {
				p := dst.NRGBAAt(x, y)
				p.A = uint8(float64(p.A) * (1 - cut))
				dst.SetNRGBA(x, y, p)
			}
			if cov := clamp01(r + 0.5 - d); cov > 0 {
				dst.SetNRGBA(x, y, over(red, cov, dst.NRGBAAt(x, y)))
			}
		}
	}
	return dst
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// over compone c (con cobertura cov) sobre b, sin premultiplicar.
func over(c color.NRGBA, cov float64, b color.NRGBA) color.NRGBA {
	sa := float64(c.A) / 255 * cov
	ba := float64(b.A) / 255
	oa := sa + ba*(1-sa)
	if oa <= 0 {
		return color.NRGBA{}
	}
	mix := func(s, d uint8) uint8 {
		return uint8(math.Round((float64(s)*sa + float64(d)*ba*(1-sa)) / oa))
	}
	return color.NRGBA{mix(c.R, b.R), mix(c.G, b.G), mix(c.B, b.B), uint8(math.Round(oa * 255))}
}

// toPixmap convierte a ARGB32 en orden de red (A, R, G, B por píxel), sin premultiplicar.
func toPixmap(img *image.NRGBA) Pixmap {
	b := img.Bounds()
	data := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p := img.NRGBAAt(x, y)
			data = append(data, p.A, p.R, p.G, p.B)
		}
	}
	return Pixmap{W: int32(b.Dx()), H: int32(b.Dy()), Data: data}
}
