package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
)

const (
	avatarID1 = "11111111-1111-1111-1111-111111111111"
	avatarID2 = "22222222-2222-2222-2222-222222222222"
)

var (
	errBoom = errors.New("сбой базы данных")
	// errAny в ожиданиях теста означает любую ошибку без проверки цепочки.
	errAny   = errors.New("любая ошибка")
	testTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	avatarCols = []string{
		"id", "user_id", "file_name", "mime_type", "size_bytes", "width", "height", "s3_key",
		"thumbnail_s3_keys", "upload_status", "processing_status", "created_at", "updated_at", "deleted_at",
	}
)

// requireErr проверяет, что err соответствует ожиданию want (см. errAny).
func requireErr(t *testing.T, want, err error) {
	t.Helper()

	require.Error(t, err)
	if !errors.Is(want, errAny) {
		require.ErrorIs(t, err, want)
	}
}

// newFakePostgres создаёт Postgres поверх поддельной базы, отвечающей по handler.
func newFakePostgres(handler fakeHandler) (*Postgres, *fakeDB) {
	db := &fakeDB{handler: handler}

	return NewPostgresFromDB(db.open()), db
}

// byQuery строит handler, который отвечает на запросы по таблице ответов;
// неизвестный запрос приводит к ошибке, что ловит опечатки в тестах.
func byQuery(responses map[string]fakeResponse) fakeHandler {
	return func(query string, _ []driver.NamedValue) fakeResponse {
		if resp, ok := responses[query]; ok {
			return resp
		}

		return fakeResponse{err: errors.New("неожиданный запрос: " + query)}
	}
}

// avatarRow возвращает строку аватарки для ответа поддельной базы.
func avatarRow(id string, thumbnails any, deletedAt any) []driver.Value {
	return []driver.Value{
		id, "user-1", "a.png", "image/png", int64(1024), int64(640), int64(480), "originals/" + id,
		thumbnails, "uploaded", "completed", testTime, testTime, deletedAt,
	}
}

// rows возвращает ответ поддельной базы со столбцами аватарки.
func rows(values ...[]driver.Value) fakeResponse {
	return fakeResponse{cols: avatarCols, rows: values}
}

// affected возвращает ответ поддельной базы на изменяющий запрос, затронувший n строк.
func affected(n int) fakeResponse {
	return fakeResponse{rows: make([][]driver.Value, n)}
}

// wantAvatar - аватарка, соответствующая avatarRow с ключами миниатюр thumbnails.
func wantAvatar(id string, thumbnails map[string]string) domain.Avatar {
	return domain.Avatar{
		CreatedAt:        testTime,
		UpdatedAt:        testTime,
		Thumbnails:       thumbnails,
		ID:               id,
		UserID:           "user-1",
		FileName:         "a.png",
		MimeType:         "image/png",
		S3Key:            "originals/" + id,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		SizeBytes:        1024,
		Width:            640,
		Height:           480,
	}
}

func TestNewPostgres_DSNValidatedLazily(t *testing.T) {
	p, err := NewPostgres("postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.Error(t, p.Ping(ctx), "база по этому адресу недоступна")
	require.NoError(t, p.Close())
}

func TestPostgres_PingAndClose(t *testing.T) {
	p, _ := newFakePostgres(byQuery(nil))
	require.NoError(t, p.Ping(context.Background()))
	require.NoError(t, p.Close())
	require.Error(t, p.Ping(context.Background()), "после Close база недоступна")
}

func TestPostgres_Migrate_ConnectionError(t *testing.T) {
	p, db := newFakePostgres(func(string, []driver.NamedValue) fakeResponse {
		return fakeResponse{err: errBoom}
	})

	// golang-migrate не оборачивает исходную ошибку через %w, поэтому
	// проверяется текст, а не цепочка ошибок
	err := p.Migrate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), errBoom.Error())
	assert.NotEmpty(t, db.executed(), "мигратор должен был обратиться к базе")
}

func TestPostgres_CreateAvatar(t *testing.T) {
	tests := []struct {
		wantErr error
		name    string
		resp    fakeResponse
	}{
		{
			name: "успех",
			resp: fakeResponse{cols: []string{"created_at", "updated_at"}, rows: [][]driver.Value{{testTime, testTime}}},
		},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, db := newFakePostgres(byQuery(map[string]fakeResponse{queryCreateAvatar: tt.resp}))

			in := wantAvatar(avatarID1, map[string]string{"100x100": "x"})
			in.CreatedAt, in.UpdatedAt = time.Time{}, time.Time{}

			got, err := p.CreateAvatar(context.Background(), in)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, wantAvatar(avatarID1, nil), got, "миниатюр у новой аватарки нет")

			require.Len(t, db.args, 1)
			args := db.args[0]
			require.Len(t, args, 10)
			assert.Equal(t, avatarID1, args[0].Value)
			assert.Equal(t, "user-1", args[1].Value)
			assert.Equal(t, "originals/"+avatarID1, args[7].Value)
			assert.Equal(t, "uploaded", args[8].Value)
			assert.Equal(t, "completed", args[9].Value)
		})
	}
}

func TestPostgres_GetAvatar(t *testing.T) {
	tests := []struct {
		wantErr error
		name    string
		resp    fakeResponse
		want    domain.Avatar
	}{
		{
			name: "с миниатюрами",
			resp: rows(avatarRow(avatarID1, []byte(`{"100x100":"t/100","300x300":"t/300"}`), nil)),
			want: wantAvatar(avatarID1, map[string]string{"100x100": "t/100", "300x300": "t/300"}),
		},
		{name: "без миниатюр", resp: rows(avatarRow(avatarID1, nil, nil)), want: wantAvatar(avatarID1, nil)},
		{name: "не найдена", resp: rows(), wantErr: domain.ErrAvatarNotFound},
		{name: "некорректный JSON миниатюр", resp: rows(avatarRow(avatarID1, []byte(`{`), nil)), wantErr: errAny},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newFakePostgres(byQuery(map[string]fakeResponse{queryGetAvatar: tt.resp}))

			got, err := p.GetAvatar(context.Background(), avatarID1)
			if tt.wantErr != nil {
				requireErr(t, tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPostgres_LatestAvatar(t *testing.T) {
	tests := []struct {
		wantErr error
		name    string
		resp    fakeResponse
	}{
		{name: "найдена", resp: rows(avatarRow(avatarID2, nil, nil))},
		{name: "аватарок нет", resp: rows(), wantErr: domain.ErrAvatarNotFound},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, db := newFakePostgres(byQuery(map[string]fakeResponse{queryLatestAvatar: tt.resp}))

			got, err := p.LatestAvatar(context.Background(), "user-1")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, wantAvatar(avatarID2, nil), got)
			assert.Equal(t, "user-1", db.args[0][0].Value)
		})
	}
}

func TestPostgres_ListAvatars(t *testing.T) {
	tests := []struct {
		wantErr error
		name    string
		resp    fakeResponse
		want    []domain.Avatar
	}{
		{
			name: "несколько аватарок",
			resp: rows(avatarRow(avatarID2, nil, nil), avatarRow(avatarID1, []byte(`{"100x100":"t"}`), nil)),
			want: []domain.Avatar{wantAvatar(avatarID2, nil), wantAvatar(avatarID1, map[string]string{"100x100": "t"})},
		},
		{name: "пусто", resp: rows()},
		{name: "ошибка чтения строки", resp: rows(avatarRow(avatarID1, []byte(`[`), nil)), wantErr: errAny},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newFakePostgres(byQuery(map[string]fakeResponse{queryListAvatars: tt.resp}))

			got, err := p.ListAvatars(context.Background(), "user-1")
			if tt.wantErr != nil {
				requireErr(t, tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPostgres_SoftDeleteAvatar(t *testing.T) {
	deletedAt := testTime.Add(time.Hour)

	tests := []struct {
		wantErr error
		name    string
		resp    fakeResponse
	}{
		{name: "удалена", resp: rows(avatarRow(avatarID1, []byte(`{"100x100":"t"}`), deletedAt))},
		{name: "не найдена или уже удалена", resp: rows(), wantErr: domain.ErrAvatarNotFound},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newFakePostgres(byQuery(map[string]fakeResponse{querySoftDelete: tt.resp}))

			got, err := p.SoftDeleteAvatar(context.Background(), avatarID1)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			want := wantAvatar(avatarID1, map[string]string{"100x100": "t"})
			want.DeletedAt = &deletedAt
			assert.Equal(t, want, got)
		})
	}
}

func TestPostgres_StatusUpdates(t *testing.T) {
	type call func(p *Postgres) error

	calls := []struct {
		call  call
		name  string
		query string
		arg   string
	}{
		{
			name:  "UpdateUploadStatus",
			query: queryUpdateUploadStatus,
			arg:   "failed",
			call: func(p *Postgres) error {
				return p.UpdateUploadStatus(context.Background(), avatarID1, domain.UploadStatusFailed)
			},
		},
		{
			name:  "UpdateProcessingStatus",
			query: queryUpdateProcessingStatus,
			arg:   "processing",
			call: func(p *Postgres) error {
				return p.UpdateProcessingStatus(context.Background(), avatarID1, domain.ProcessingStatusProcessing)
			},
		},
		{
			name:  "CompleteProcessing",
			query: queryCompleteProcessing,
			arg:   `{"100x100":"t/100"}`,
			call: func(p *Postgres) error {
				return p.CompleteProcessing(context.Background(), avatarID1, map[string]string{"100x100": "t/100"})
			},
		},
	}

	results := []struct {
		wantErr error
		name    string
		resp    fakeResponse
	}{
		{name: "успех", resp: affected(1)},
		{name: "не найдена", resp: affected(0), wantErr: domain.ErrAvatarNotFound},
		{name: "сбой базы", resp: fakeResponse{err: errBoom}, wantErr: errBoom},
	}

	for _, c := range calls {
		for _, r := range results {
			t.Run(c.name+"/"+r.name, func(t *testing.T) {
				p, db := newFakePostgres(byQuery(map[string]fakeResponse{c.query: r.resp}))

				err := c.call(p)
				if r.wantErr != nil {
					require.ErrorIs(t, err, r.wantErr)
					return
				}

				require.NoError(t, err)
				require.Len(t, db.args, 1)
				assert.Equal(t, avatarID1, db.args[0][0].Value)
				assert.Equal(t, c.arg, db.args[0][1].Value)
			})
		}
	}
}

func TestPostgres_ProcessedMessages(t *testing.T) {
	t.Run("IsMessageProcessed", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			p, _ := newFakePostgres(byQuery(map[string]fakeResponse{
				queryIsMessageProcessed: {cols: []string{"exists"}, rows: [][]driver.Value{{want}}},
			}))

			got, err := p.IsMessageProcessed(context.Background(), "m-1")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}
	})

	t.Run("IsMessageProcessed: сбой базы", func(t *testing.T) {
		p, _ := newFakePostgres(byQuery(map[string]fakeResponse{queryIsMessageProcessed: {err: errBoom}}))

		_, err := p.IsMessageProcessed(context.Background(), "m-1")
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("MarkMessageProcessed", func(t *testing.T) {
		p, db := newFakePostgres(byQuery(map[string]fakeResponse{queryMarkMessageProcessed: affected(0)}))

		require.NoError(t, p.MarkMessageProcessed(context.Background(), "m-1"),
			"повторная отметка (0 затронутых строк) ошибкой не считается")
		assert.Equal(t, "m-1", db.args[0][0].Value)
	})

	t.Run("MarkMessageProcessed: сбой базы", func(t *testing.T) {
		p, _ := newFakePostgres(byQuery(map[string]fakeResponse{queryMarkMessageProcessed: {err: errBoom}}))

		require.ErrorIs(t, p.MarkMessageProcessed(context.Background(), "m-1"), errBoom)
	})
}
