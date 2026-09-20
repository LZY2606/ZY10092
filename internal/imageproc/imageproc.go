package imageproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"tileforge/internal/geo"
)

const OutputSize = 256

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

func DecodePNG(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if format != "png" {
		return nil, format, fmt.Errorf("only PNG tiles are supported, got %s", format)
	}
	return img, format, nil
}

func PixelHash(img image.Image) string {
	bounds := img.Bounds()
	values := make([]byte, 0, bounds.Dx()*bounds.Dy()*4)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			values = append(values, byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8))
		}
	}
	sum := sha256.Sum256(values)
	return fmt.Sprintf("sha256:%x", sum)
}

func RenderTransparent() ([]byte, image.Image, error) {
	img := image.NewRGBA(image.Rect(0, 0, OutputSize, OutputSize))
	return encodePNG(img)
}

func RenderTile(source geo.SourceTile, target geo.BBox, sourceImage image.Image) ([]byte, image.Image, error) {
	output := image.NewRGBA(image.Rect(0, 0, OutputSize, OutputSize))
	bounds := sourceImage.Bounds()
	for py := 0; py < OutputSize; py++ {
		for px := 0; px < OutputSize; px++ {
			lon := target.West + (float64(px)+0.5)/OutputSize*(target.East-target.West)
			lat := target.North - (float64(py)+0.5)/OutputSize*(target.North-target.South)
			u, v, ok := projectToSource(lon, lat, source)
			if !ok {
				continue
			}
			sx := bounds.Min.X + int(math.Floor(u*float64(bounds.Dx())))
			sy := bounds.Min.Y + int(math.Floor(v*float64(bounds.Dy())))
			sx = min(bounds.Max.X-1, max(bounds.Min.X, sx))
			sy = min(bounds.Max.Y-1, max(bounds.Min.Y, sy))
			output.Set(px, py, sourceImage.At(sx, sy))
		}
	}
	return encodePNG(output)
}

func projectToSource(lon, lat float64, source geo.SourceTile) (float64, float64, bool) {
	if lon < source.BBox.West || lon >= source.BBox.East || lat <= source.BBox.South || lat > source.BBox.North {
		return 0, 0, false
	}
	u := (lon - source.BBox.West) / (source.BBox.East - source.BBox.West)
	var v float64
	if source.CRS == geo.EPSG4326 {
		v = (source.BBox.North - lat) / (source.BBox.North - source.BBox.South)
	} else {
		v = mercatorV(lat, source.BBox)
	}
	return u, v, true
}

func mercatorV(lat float64, b geo.BBox) float64 {
	north := mercatorY(b.North)
	south := mercatorY(b.South)
	current := mercatorY(lat)
	return (north - current) / (north - south)
}

func mercatorY(lat float64) float64 {
	rad := lat * math.Pi / 180
	return math.Log(math.Tan(math.Pi/4 + rad/2))
}

func encodePNG(img image.Image) ([]byte, image.Image, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), img, nil
}

func EdgePixelsEqual(a, b image.Image, edge string) bool {
	ba, bb := a.Bounds(), b.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return false
	}
	for i := 0; i < ba.Dx(); i++ {
		var ca, cb color.Color
		switch edge {
		case "vertical":
			ca = a.At(ba.Min.X+ba.Dx()-1, ba.Min.Y+i)
			cb = b.At(bb.Min.X, bb.Min.Y+i)
		case "horizontal":
			ca = a.At(ba.Min.X+i, ba.Min.Y+ba.Dy()-1)
			cb = b.At(bb.Min.X+i, bb.Min.Y)
		default:
			return false
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func CanonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}
