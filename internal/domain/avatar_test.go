package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeys(t *testing.T) {
	assert.Equal(t, "originals/a.png", OriginalKey("a", "png"))
	assert.Equal(t, "thumbnails/a/100x100.webp", ThumbnailKey("a", "100x100", "webp"))

	avatar := Avatar{
		S3Key: "originals/a.png",
		Thumbnails: map[string]string{
			"300x300": "thumbnails/a/300x300.png",
			"100x100": "thumbnails/a/100x100.png",
		},
	}
	assert.Equal(t, []string{"originals/a.png", "thumbnails/a/100x100.png", "thumbnails/a/300x300.png"}, avatar.Keys(),
		"оригинал первым, миниатюры в стабильном порядке")

	assert.Equal(t, []string{"originals/b.jpg"}, Avatar{S3Key: "originals/b.jpg"}.Keys())
}
