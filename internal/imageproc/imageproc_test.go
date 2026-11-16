package imageproc

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/chai2010/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample создаёт тестовое изображение width x height в формате format.
func sample(t *testing.T, format Format, width, height int) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}

	var buf bytes.Buffer
	var err error
	switch format {
	case FormatJPEG:
		err = jpeg.Encode(&buf, img, nil)
	case FormatPNG:
		err = png.Encode(&buf, img)
	case FormatWebP:
		err = webp.Encode(&buf, img, &webp.Options{Quality: 90})
	default:
		t.Fatalf("неизвестный формат %q", format)
	}
	require.NoError(t, err)

	return buf.Bytes()
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Format
		wantErr bool
	}{
		{name: "jpeg", input: "jpeg", want: FormatJPEG},
		{name: "jpg в верхнем регистре", input: "JPG", want: FormatJPEG},
		{name: "png", input: "png", want: FormatPNG},
		{name: "webp", input: "WebP", want: FormatWebP},
		{name: "gif не поддерживается", input: "gif", wantErr: true},
		{name: "пусто", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFormat(tt.input)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedFormat)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormat_MimeTypeAndExtension(t *testing.T) {
	tests := []struct {
		format Format
		mime   string
		ext    string
	}{
		{format: FormatJPEG, mime: "image/jpeg", ext: "jpg"},
		{format: FormatPNG, mime: "image/png", ext: "png"},
		{format: FormatWebP, mime: "image/webp", ext: "webp"},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			assert.Equal(t, tt.mime, tt.format.MimeType())
			assert.Equal(t, tt.ext, tt.format.Extension())

			got, err := FormatFromMimeType(tt.mime)
			require.NoError(t, err)
			assert.Equal(t, tt.format, got)
		})
	}

	_, err := FormatFromMimeType("image/gif")
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Size
		wantErr bool
	}{
		{name: "100x100", input: "100x100", want: ThumbnailSizes[0]},
		{name: "300x300", input: "300x300", want: ThumbnailSizes[1]},
		{name: "неизвестный размер", input: "200x200", wantErr: true},
		{name: "оригинал - не миниатюра", input: SizeOriginal, wantErr: true},
		{name: "без высоты", input: "100x", wantErr: true},
		{name: "мусор", input: "abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidSize)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInspect(t *testing.T) {
	var gifBuf bytes.Buffer
	require.NoError(t, gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black}), nil))

	tests := []struct {
		wantErr error
		name    string
		data    []byte
		want    Info
	}{
		{name: "jpeg", data: sample(t, FormatJPEG, 40, 30), want: Info{Format: FormatJPEG, Width: 40, Height: 30}},
		{name: "png", data: sample(t, FormatPNG, 20, 50), want: Info{Format: FormatPNG, Width: 20, Height: 50}},
		{name: "webp", data: sample(t, FormatWebP, 64, 32), want: Info{Format: FormatWebP, Width: 64, Height: 32}},
		{name: "gif", data: gifBuf.Bytes(), wantErr: ErrUnsupportedFormat},
		{name: "текст", data: []byte("hello, world"), wantErr: ErrUnsupportedFormat},
		{name: "обрезанный png", data: sample(t, FormatPNG, 20, 20)[:10], wantErr: ErrUnsupportedFormat},
		{name: "пусто", data: nil, wantErr: ErrUnsupportedFormat},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Inspect(tt.data)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInspect_TooManyPixels(t *testing.T) {
	// PNG-заголовок с огромными размерами: пиксели не декодируются, поэтому
	// проверка срабатывает до выделения памяти.
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))))
	data := buf.Bytes()
	// IHDR: тип чанка со смещения 12, ширина и высота по 4 байта со смещения
	// 16, 13 байт данных и за ними CRC, которую нужно пересчитать.
	copy(data[16:24], []byte{0, 0, 0x27, 0x10, 0, 0, 0x27, 0x10}) // 10000x10000
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))

	_, err := Inspect(data)
	require.ErrorIs(t, err, ErrTooManyPixels)
}

func TestThumbnail(t *testing.T) {
	sources := []Format{FormatJPEG, FormatPNG, FormatWebP}
	targets := []Format{FormatJPEG, FormatPNG, FormatWebP}

	for _, src := range sources {
		for _, dst := range targets {
			for _, size := range ThumbnailSizes {
				t.Run(string(src)+"->"+string(dst)+"/"+size.Name, func(t *testing.T) {
					got, err := Thumbnail(sample(t, src, 640, 480), size, dst)
					require.NoError(t, err)

					info, err := Inspect(got)
					require.NoError(t, err)
					assert.Equal(t, Info{Format: dst, Width: size.Width, Height: size.Height}, info)
				})
			}
		}
	}
}

func TestThumbnail_InvalidInput(t *testing.T) {
	_, err := Thumbnail([]byte("not an image"), ThumbnailSizes[0], FormatPNG)
	require.ErrorIs(t, err, ErrUnsupportedFormat)

	_, err = Thumbnail(sample(t, FormatPNG, 10, 10), ThumbnailSizes[0], Format("bmp"))
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestConvert(t *testing.T) {
	got, err := Convert(sample(t, FormatPNG, 33, 17), FormatWebP)
	require.NoError(t, err)

	info, err := Inspect(got)
	require.NoError(t, err)
	assert.Equal(t, Info{Format: FormatWebP, Width: 33, Height: 17}, info)

	_, err = Convert([]byte("x"), FormatPNG)
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestEncode_JPEGFlattensTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4)) // полностью прозрачное

	data, err := encode(img, FormatJPEG)
	require.NoError(t, err)

	decoded, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	r, g, b, _ := decoded.At(1, 1).RGBA()
	assert.Greater(t, r>>8, uint32(240), "прозрачное становится белым, а не чёрным")
	assert.Greater(t, g>>8, uint32(240))
	assert.Greater(t, b>>8, uint32(240))
}
