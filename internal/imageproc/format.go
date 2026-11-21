// Package imageproc определяет формат изображений, проверяет их и создаёт
// миниатюры. Поддерживаются JPEG, PNG и WebP; WebP кодируется через libwebp
// (cgo), поэтому пакет требует CGO_ENABLED=1.
package imageproc

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ErrUnsupportedFormat возвращается для изображений и названий форматов,
// отличных от JPEG, PNG и WebP.
var ErrUnsupportedFormat = errors.New("неподдерживаемый формат изображения")

// ErrInvalidSize возвращается для некорректного названия размера миниатюры.
var ErrInvalidSize = errors.New("некорректный размер изображения")

// Format - формат изображения.
type Format string

const (
	// FormatJPEG - JPEG.
	FormatJPEG Format = "jpeg"
	// FormatPNG - PNG.
	FormatPNG Format = "png"
	// FormatWebP - WebP.
	FormatWebP Format = "webp"
)

// mimeTypes - MIME-типы поддерживаемых форматов.
var mimeTypes = map[Format]string{
	FormatJPEG: "image/jpeg",
	FormatPNG:  "image/png",
	FormatWebP: "image/webp",
}

// MimeType возвращает MIME-тип формата.
func (f Format) MimeType() string {
	return mimeTypes[f]
}

// Extension возвращает расширение файла формата без точки.
func (f Format) Extension() string {
	if f == FormatJPEG {
		return "jpg"
	}

	return string(f)
}

// ParseFormat разбирает название формата: "jpeg" (или "jpg"), "png", "webp";
// регистр не важен.
func ParseFormat(name string) (Format, error) {
	switch strings.ToLower(name) {
	case "jpeg", "jpg":
		return FormatJPEG, nil
	case "png":
		return FormatPNG, nil
	case "webp":
		return FormatWebP, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedFormat, name)
	}
}

// FormatFromMimeType возвращает формат по MIME-типу.
func FormatFromMimeType(mimeType string) (Format, error) {
	for format, mt := range mimeTypes {
		if mt == mimeType {
			return format, nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrUnsupportedFormat, mimeType)
}

// Detect определяет формат изображения по сигнатуре содержимого, а не по
// имени файла или заявленному клиентом типу.
func Detect(data []byte) (Format, error) {
	return FormatFromMimeType(http.DetectContentType(data))
}

// Size - размер миниатюры.
type Size struct {
	// Name - название размера в API и ключах хранилища ("100x100").
	Name string
	// Width - ширина в пикселях.
	Width int
	// Height - высота в пикселях.
	Height int
}

// ThumbnailSizes - размеры миниатюр, которые создаются для каждой аватарки.
var ThumbnailSizes = []Size{
	{Name: "100x100", Width: 100, Height: 100},
	{Name: "300x300", Width: 300, Height: 300},
}

// SizeOriginal - название размера, означающее оригинал изображения.
const SizeOriginal = "original"

// ParseSize разбирает название размера вида "ШИРИНАxВЫСОТА" и проверяет, что
// это один из ThumbnailSizes.
func ParseSize(name string) (Size, error) {
	w, h, ok := strings.Cut(name, "x")
	width, errW := strconv.Atoi(w)
	height, errH := strconv.Atoi(h)
	if !ok || errW != nil || errH != nil {
		return Size{}, fmt.Errorf("%w: %q", ErrInvalidSize, name)
	}

	for _, size := range ThumbnailSizes {
		if size.Width == width && size.Height == height {
			return size, nil
		}
	}

	return Size{}, fmt.Errorf("%w: %q", ErrInvalidSize, name)
}
