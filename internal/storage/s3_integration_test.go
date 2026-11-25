//go:build integration

package storage

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

// S3Suite проверяет S3 на настоящем MinIO в контейнере.
type S3Suite struct {
	suite.Suite
	container *tcminio.MinioContainer
	s3        *S3
}

func TestS3Suite(t *testing.T) {
	suite.Run(t, new(S3Suite))
}

func (s *S3Suite) SetupSuite() {
	ctx := context.Background()

	container, err := tcminio.Run(ctx, "pgsty/minio:RELEASE.2026-08-04T00-00-00Z",
		tcminio.WithUsername("minioadmin"), tcminio.WithPassword("minioadmin"))
	s.Require().NoError(err)
	s.container = container

	endpoint, err := container.ConnectionString(ctx)
	s.Require().NoError(err)

	s.s3, err = NewS3(Config{
		Endpoint:  endpoint,
		AccessKey: container.Username,
		SecretKey: container.Password,
		Bucket:    testBucket,
	})
	s.Require().NoError(err)
	s.Require().NoError(s.s3.EnsureBucket(ctx))
	s.Require().NoError(s.s3.EnsureBucket(ctx), "повторное создание бакета")
}

func (s *S3Suite) TearDownSuite() {
	testcontainers.CleanupContainer(s.T(), s.container)
}

func (s *S3Suite) TestObjectLifecycle() {
	ctx := context.Background()
	s.Require().NoError(s.s3.Ping(ctx))

	payload := []byte("png-bytes")
	s.Require().NoError(s.s3.Put(ctx, "originals/1", bytes.NewReader(payload), int64(len(payload)), "image/png"))

	obj, err := s.s3.Get(ctx, "originals/1")
	s.Require().NoError(err)
	s.Equal("image/png", obj.ContentType)
	s.Equal(int64(len(payload)), obj.Size)
	s.NotEmpty(obj.ETag)
	s.Require().NoError(obj.Body.Close())

	data, err := s.s3.Download(ctx, "originals/1")
	s.Require().NoError(err)
	s.Equal(payload, data)

	s.Require().NoError(s.s3.Delete(ctx, "originals/1", "thumbnails/1/100x100"))
	s.Require().NoError(s.s3.Delete(ctx, "originals/1"), "повторное удаление")

	_, err = s.s3.Get(ctx, "originals/1")
	s.ErrorIs(err, ErrObjectNotFound)
}
