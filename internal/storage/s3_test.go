package storage

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBucket = "avatars"

func TestNewS3_InvalidEndpoint(t *testing.T) {
	_, err := NewS3(Config{Endpoint: "ftp://host:9000", Bucket: testBucket})
	require.Error(t, err, "неподдерживаемая схема")
}

func TestS3_EnsureBucketAndPing(t *testing.T) {
	s, fake := newFakeS3(t, testBucket)
	ctx := context.Background()

	require.Error(t, s.Ping(ctx), "бакета ещё нет")

	require.NoError(t, s.EnsureBucket(ctx))
	require.NoError(t, s.EnsureBucket(ctx), "повторный вызов при существующем бакете")
	require.NoError(t, s.Ping(ctx))

	fake.setFail(http.StatusInternalServerError)
	require.Error(t, s.Ping(ctx))
	require.Error(t, s.EnsureBucket(ctx))
}

func TestS3_PutGetDownload(t *testing.T) {
	s, _ := newFakeS3(t, testBucket)
	ctx := context.Background()
	require.NoError(t, s.EnsureBucket(ctx))

	require.NoError(t, s.Put(ctx, "originals/1", strings.NewReader("image-bytes"), 11, "image/png"))

	obj, err := s.Get(ctx, "originals/1")
	require.NoError(t, err)
	data, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, "image-bytes", string(data))
	assert.Equal(t, "image/png", obj.ContentType)
	assert.Equal(t, int64(11), obj.Size)
	assert.NotEmpty(t, obj.ETag)

	data, err = s.Download(ctx, "originals/1")
	require.NoError(t, err)
	assert.Equal(t, "image-bytes", string(data))

	require.NoError(t, s.Put(ctx, "originals/1", strings.NewReader("new"), 3, "image/jpeg"), "перезапись")
	data, err = s.Download(ctx, "originals/1")
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}

func TestS3_GetNotFound(t *testing.T) {
	s, _ := newFakeS3(t, testBucket)
	ctx := context.Background()
	require.NoError(t, s.EnsureBucket(ctx))

	_, err := s.Get(ctx, "missing")
	require.ErrorIs(t, err, ErrObjectNotFound)

	_, err = s.Download(ctx, "missing")
	require.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3_Errors(t *testing.T) {
	t.Run("сбой хранилища", func(t *testing.T) {
		s, fake := newFakeS3(t, testBucket)
		ctx := context.Background()
		require.NoError(t, s.EnsureBucket(ctx))
		fake.setFail(http.StatusForbidden)

		require.Error(t, s.Put(ctx, "k", strings.NewReader("x"), 1, "image/png"))
		_, err := s.Get(ctx, "k")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrObjectNotFound)
		require.Error(t, s.Delete(ctx, "k"))
	})
}

func TestS3_Delete(t *testing.T) {
	s, _ := newFakeS3(t, testBucket)
	ctx := context.Background()
	require.NoError(t, s.EnsureBucket(ctx))

	for _, key := range []string{"a", "b"} {
		require.NoError(t, s.Put(ctx, key, strings.NewReader("x"), 1, "image/png"))
	}

	require.NoError(t, s.Delete(ctx, "a", "b", "missing"))
	require.NoError(t, s.Delete(ctx, "a"), "повторное удаление безопасно")
	require.NoError(t, s.Delete(ctx), "пустой список")

	_, err := s.Get(ctx, "a")
	require.ErrorIs(t, err, ErrObjectNotFound)
}
