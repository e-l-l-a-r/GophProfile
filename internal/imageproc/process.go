package imageproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"github.com/chai2010/webp" // регистрирует декодер WebP в пакете image
	"github.com/disintegration/imaging"
)

// MaxPixels - предельное число пикселей изображения. Ограничение защищает от
// изображений, которые занимают мало байт, но огромны после декодирования.
const MaxPixels = 50_000_000

// quality - качество сжатия JPEG и WebP с потерями.
const quality = 85

// ErrTooManyPixels возвращается для изображений больше MaxPixels пикселей.
var ErrTooManyPixels = errors.New("слишком большое разрешение изображения")

// Info - сведения об изображении.
type Info struct {
	// Format - формат изображения.
	Format Format
	// Width - ширина в пикселях.
	Width int
	// Height - высота в пикселях.
	Height int
}

// Inspect проверяет изображение и возвращает его формат и размеры, не
// декодируя пиксели. Возвращает ErrUnsupportedFormat для неподдерживаемого
// формата и ErrTooManyPixels для слишком большого разрешения.
func Inspect(data []byte) (Info, error) {
	format, err := Detect(data)
	if err != nil {
		return Info{}, err
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Info{}, fmt.Errorf("%w: не удалось прочитать заголовок: %w", ErrUnsupportedFormat, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, fmt.Errorf("%w: пустое изображение", ErrUnsupportedFormat)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return Info{}, fmt.Errorf("%w: %dx%d", ErrTooManyPixels, cfg.Width, cfg.Height)
	}

	return Info{Format: format, Width: cfg.Width, Height: cfg.Height}, nil
}

// Thumbnail создаёт миниатюру размера size в формате format: изображение
// масштабируется с сохранением пропорций и обрезается по центру.
func Thumbnail(data []byte, size Size, format Format) ([]byte, error) {
	img, err := decode(data)
	if err != nil {
		return nil, err
	}

	return encode(imaging.Fill(img, size.Width, size.Height, imaging.Center, imaging.Lanczos), format)
}

// Convert перекодирует изображение в формат format без изменения размеров.
func Convert(data []byte, format Format) ([]byte, error) {
	img, err := decode(data)
	if err != nil {
		return nil, err
	}

	return encode(img, format)
}

// decode проверяет и декодирует изображение.
func decode(data []byte) (image.Image, error) {
	if _, err := Inspect(data); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("не удалось декодировать изображение: %w", err)
	}

	return img, nil
}

// encode кодирует изображение в формат format.
func encode(img image.Image, format Format) ([]byte, error) {
	var (
		buf bytes.Buffer
		err error
	)
	switch format {
	case FormatJPEG:
		// JPEG не поддерживает прозрачность: прозрачные области заливаются белым,
		// иначе они стали бы чёрными.
		flat := imaging.New(img.Bounds().Dx(), img.Bounds().Dy(), color.White)
		err = jpeg.Encode(&buf, imaging.Overlay(flat, img, image.Point{}, 1), &jpeg.Options{Quality: quality})
	case FormatPNG:
		err = png.Encode(&buf, img)
	case FormatWebP:
		err = webp.Encode(&buf, img, &webp.Options{Quality: quality})
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось закодировать изображение в %s: %w", format, err)
	}

	return buf.Bytes(), nil
}
