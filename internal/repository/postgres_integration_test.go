//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
)

// PostgresSuite проверяет Postgres на настоящем PostgreSQL в контейнере.
type PostgresSuite struct {
	suite.Suite
	container *tcpostgres.PostgresContainer
	repo      *Postgres
}

func TestPostgresSuite(t *testing.T) {
	suite.Run(t, new(PostgresSuite))
}

func (s *PostgresSuite) SetupSuite() {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	s.Require().NoError(err)
	s.container = container

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	s.repo, err = NewPostgres(dsn)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Migrate())
	s.Require().NoError(s.repo.Migrate(), "повторное применение миграций не ошибка")
}

func (s *PostgresSuite) TearDownSuite() {
	if s.repo != nil {
		s.NoError(s.repo.Close())
	}
	testcontainers.CleanupContainer(s.T(), s.container)
}

func (s *PostgresSuite) SetupTest() {
	_, err := s.repo.db.Exec(`TRUNCATE avatars, processed_messages`)
	s.Require().NoError(err)
}

// create сохраняет аватарку пользователя userID с идентификатором id.
func (s *PostgresSuite) create(id, userID string) domain.Avatar {
	avatar, err := s.repo.CreateAvatar(context.Background(), domain.Avatar{
		ID:               id,
		UserID:           userID,
		FileName:         "a.png",
		MimeType:         "image/png",
		S3Key:            "originals/" + id,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
		SizeBytes:        100,
		Width:            10,
		Height:           20,
	})
	s.Require().NoError(err)

	return avatar
}

func (s *PostgresSuite) TestAvatarLifecycle() {
	ctx := context.Background()

	created := s.create(avatarID1, "user-1")
	s.False(created.CreatedAt.IsZero())

	got, err := s.repo.GetAvatar(ctx, avatarID1)
	s.Require().NoError(err)
	s.Equal(created.ID, got.ID)
	s.Equal(10, got.Width)
	s.Nil(got.Thumbnails)
	s.Equal(domain.ProcessingStatusPending, got.ProcessingStatus)

	s.Require().NoError(s.repo.UpdateProcessingStatus(ctx, avatarID1, domain.ProcessingStatusProcessing))
	thumbs := map[string]string{"100x100": "thumbnails/1/100x100", "300x300": "thumbnails/1/300x300"}
	s.Require().NoError(s.repo.CompleteProcessing(ctx, avatarID1, thumbs))

	got, err = s.repo.GetAvatar(ctx, avatarID1)
	s.Require().NoError(err)
	s.Equal(thumbs, got.Thumbnails)
	s.Equal(domain.ProcessingStatusCompleted, got.ProcessingStatus)
	s.True(got.UpdatedAt.After(created.UpdatedAt) || got.UpdatedAt.Equal(created.UpdatedAt))

	deleted, err := s.repo.SoftDeleteAvatar(ctx, avatarID1)
	s.Require().NoError(err)
	s.Require().NotNil(deleted.DeletedAt)
	s.Equal(thumbs, deleted.Thumbnails)

	_, err = s.repo.GetAvatar(ctx, avatarID1)
	s.ErrorIs(err, domain.ErrAvatarNotFound)
	_, err = s.repo.SoftDeleteAvatar(ctx, avatarID1)
	s.ErrorIs(err, domain.ErrAvatarNotFound, "повторное удаление")
	s.ErrorIs(s.repo.UpdateUploadStatus(ctx, avatarID1, domain.UploadStatusFailed), domain.ErrAvatarNotFound)
}

func (s *PostgresSuite) TestListAndLatest() {
	ctx := context.Background()

	_, err := s.repo.LatestAvatar(ctx, "user-1")
	s.ErrorIs(err, domain.ErrAvatarNotFound)

	s.create(avatarID1, "user-1")
	time.Sleep(10 * time.Millisecond)
	s.create(avatarID2, "user-1")
	s.create("33333333-3333-3333-3333-333333333333", "user-2")

	list, err := s.repo.ListAvatars(ctx, "user-1")
	s.Require().NoError(err)
	s.Require().Len(list, 2)
	s.Equal(avatarID2, list[0].ID, "сначала новые")
	s.Equal(avatarID1, list[1].ID)

	latest, err := s.repo.LatestAvatar(ctx, "user-1")
	s.Require().NoError(err)
	s.Equal(avatarID2, latest.ID)

	_, err = s.repo.SoftDeleteAvatar(ctx, avatarID2)
	s.Require().NoError(err)

	latest, err = s.repo.LatestAvatar(ctx, "user-1")
	s.Require().NoError(err)
	s.Equal(avatarID1, latest.ID, "удалённая аватарка пропускается")

	list, err = s.repo.ListAvatars(ctx, "user-1")
	s.Require().NoError(err)
	s.Len(list, 1)
}

func (s *PostgresSuite) TestProcessedMessages() {
	ctx := context.Background()

	processed, err := s.repo.IsMessageProcessed(ctx, "m-1")
	s.Require().NoError(err)
	s.False(processed)

	s.Require().NoError(s.repo.MarkMessageProcessed(ctx, "m-1"))
	s.Require().NoError(s.repo.MarkMessageProcessed(ctx, "m-1"), "повторная отметка")

	processed, err = s.repo.IsMessageProcessed(ctx, "m-1")
	s.Require().NoError(err)
	s.True(processed)
}
