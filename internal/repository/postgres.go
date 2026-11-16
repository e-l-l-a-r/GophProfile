// Package repository содержит хранилище метаданных аватарок в PostgreSQL и
// учёт обработанных воркером сообщений брокера.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // драйвер "pgx" для database/sql

	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/migrations"
)

// avatarColumns - столбцы аватарки в порядке, который ожидает scanAvatar.
const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, width, height, s3_key,
thumbnail_s3_keys, upload_status, processing_status, created_at, updated_at, deleted_at`

const (
	queryCreateAvatar = `INSERT INTO avatars
(id, user_id, file_name, mime_type, size_bytes, width, height, s3_key, upload_status, processing_status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING created_at, updated_at`

	queryGetAvatar = `SELECT ` + avatarColumns + `
FROM avatars
WHERE id = $1 AND deleted_at IS NULL`

	queryLatestAvatar = `SELECT ` + avatarColumns + `
FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id
LIMIT 1`

	queryListAvatars = `SELECT ` + avatarColumns + `
FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id`

	querySoftDelete = `UPDATE avatars
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING ` + avatarColumns

	queryUpdateUploadStatus = `UPDATE avatars
SET upload_status = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL`

	queryUpdateProcessingStatus = `UPDATE avatars
SET processing_status = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL`

	queryCompleteProcessing = `UPDATE avatars
SET thumbnail_s3_keys = $2::jsonb, processing_status = 'completed', updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL`

	queryIsMessageProcessed = `SELECT EXISTS (SELECT 1 FROM processed_messages WHERE message_id = $1)`

	queryMarkMessageProcessed = `INSERT INTO processed_messages (message_id)
VALUES ($1)
ON CONFLICT (message_id) DO NOTHING`
)

// Postgres - хранилище метаданных аватарок в PostgreSQL.
type Postgres struct {
	db *sql.DB
}

// NewPostgres открывает подключение к PostgreSQL по строке подключения dsn.
// Само подключение устанавливается лениво: для проверки доступности базы
// вызовите Ping, для создания таблиц - Migrate.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть подключение к базе данных: %w", err)
	}

	return NewPostgresFromDB(db), nil
}

// NewPostgresFromDB создаёт хранилище поверх готового подключения db.
// Владение подключением переходит хранилищу: Close закроет db.
func NewPostgresFromDB(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

// Ping проверяет доступность базы данных.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("база данных недоступна: %w", err)
	}

	return nil
}

// Close закрывает подключение к базе данных.
func (p *Postgres) Close() error {
	if err := p.db.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть подключение к базе данных: %w", err)
	}

	return nil
}

// Migrate применяет к базе данных все ещё не применённые миграции из пакета
// migrations. Повторный вызов без новых миграций ошибкой не считается.
func (p *Postgres) Migrate() error {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("не удалось открыть источник миграций: %w", err)
	}

	driver, err := migratepg.WithInstance(p.db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("не удалось создать драйвер миграций: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("не удалось создать мигратор: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("не удалось применить миграции: %w", err)
	}

	return nil
}

// CreateAvatar сохраняет новую аватарку с заданным идентификатором и
// возвращает её с заполненными CreatedAt и UpdatedAt. Миниатюры у новой
// аватарки отсутствуют.
func (p *Postgres) CreateAvatar(ctx context.Context, avatar domain.Avatar) (domain.Avatar, error) {
	err := p.db.QueryRowContext(ctx, queryCreateAvatar,
		avatar.ID, avatar.UserID, avatar.FileName, avatar.MimeType, avatar.SizeBytes,
		avatar.Width, avatar.Height, avatar.S3Key, avatar.UploadStatus, avatar.ProcessingStatus,
	).Scan(&avatar.CreatedAt, &avatar.UpdatedAt)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("не удалось создать аватарку: %w", err)
	}

	avatar.Thumbnails = nil
	avatar.DeletedAt = nil

	return avatar, nil
}

// GetAvatar возвращает неудалённую аватарку по идентификатору или
// domain.ErrAvatarNotFound.
func (p *Postgres) GetAvatar(ctx context.Context, id string) (domain.Avatar, error) {
	avatar, err := scanAvatar(p.db.QueryRowContext(ctx, queryGetAvatar, id))
	if err != nil {
		return domain.Avatar{}, notFoundOr(err, "не удалось получить аватарку")
	}

	return avatar, nil
}

// LatestAvatar возвращает самую новую неудалённую аватарку пользователя или
// domain.ErrAvatarNotFound, если аватарок у него нет.
func (p *Postgres) LatestAvatar(ctx context.Context, userID string) (domain.Avatar, error) {
	avatar, err := scanAvatar(p.db.QueryRowContext(ctx, queryLatestAvatar, userID))
	if err != nil {
		return domain.Avatar{}, notFoundOr(err, "не удалось получить аватарку пользователя")
	}

	return avatar, nil
}

// ListAvatars возвращает неудалённые аватарки пользователя от новых к старым.
func (p *Postgres) ListAvatars(ctx context.Context, userID string) ([]domain.Avatar, error) {
	rows, err := p.db.QueryContext(ctx, queryListAvatars, userID)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить список аватарок: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var avatars []domain.Avatar
	for rows.Next() {
		avatar, err := scanAvatar(rows)
		if err != nil {
			return nil, fmt.Errorf("не удалось прочитать аватарку: %w", err)
		}
		avatars = append(avatars, avatar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("не удалось прочитать список аватарок: %w", err)
	}

	return avatars, nil
}

// SoftDeleteAvatar помечает аватарку удалённой и возвращает её состояние после
// удаления: по нему вызывающий узнаёт ключи файлов для удаления из хранилища.
// Возвращает domain.ErrAvatarNotFound, если аватарки нет или она уже удалена.
func (p *Postgres) SoftDeleteAvatar(ctx context.Context, id string) (domain.Avatar, error) {
	avatar, err := scanAvatar(p.db.QueryRowContext(ctx, querySoftDelete, id))
	if err != nil {
		return domain.Avatar{}, notFoundOr(err, "не удалось удалить аватарку")
	}

	return avatar, nil
}

// UpdateUploadStatus меняет состояние загрузки неудалённой аватарки.
// Возвращает domain.ErrAvatarNotFound, если аватарки нет или она удалена.
func (p *Postgres) UpdateUploadStatus(ctx context.Context, id string, status domain.UploadStatus) error {
	return p.execOne(ctx, "не удалось изменить состояние загрузки", queryUpdateUploadStatus, id, status)
}

// UpdateProcessingStatus меняет состояние обработки неудалённой аватарки.
// Возвращает domain.ErrAvatarNotFound, если аватарки нет или она удалена.
func (p *Postgres) UpdateProcessingStatus(ctx context.Context, id string, status domain.ProcessingStatus) error {
	return p.execOne(ctx, "не удалось изменить состояние обработки", queryUpdateProcessingStatus, id, status)
}

// CompleteProcessing сохраняет ключи миниатюр и переводит обработку
// неудалённой аватарки в состояние completed. Возвращает
// domain.ErrAvatarNotFound, если аватарки нет или она удалена.
func (p *Postgres) CompleteProcessing(ctx context.Context, id string, thumbnails map[string]string) error {
	keys, err := json.Marshal(thumbnails)
	if err != nil {
		return fmt.Errorf("не удалось сериализовать ключи миниатюр: %w", err)
	}

	return p.execOne(ctx, "не удалось завершить обработку", queryCompleteProcessing, id, string(keys))
}

// IsMessageProcessed сообщает, обработано ли уже сообщение брокера с
// идентификатором messageID.
func (p *Postgres) IsMessageProcessed(ctx context.Context, messageID string) (bool, error) {
	var processed bool
	if err := p.db.QueryRowContext(ctx, queryIsMessageProcessed, messageID).Scan(&processed); err != nil {
		return false, fmt.Errorf("не удалось проверить обработку сообщения: %w", err)
	}

	return processed, nil
}

// MarkMessageProcessed запоминает, что сообщение брокера с идентификатором
// messageID обработано. Повторная отметка ошибкой не считается.
func (p *Postgres) MarkMessageProcessed(ctx context.Context, messageID string) error {
	if _, err := p.db.ExecContext(ctx, queryMarkMessageProcessed, messageID); err != nil {
		return fmt.Errorf("не удалось отметить сообщение обработанным: %w", err)
	}

	return nil
}

// execOne выполняет изменяющий запрос, который должен затронуть ровно одну
// аватарку, и возвращает domain.ErrAvatarNotFound, если не затронул ни одной.
func (p *Postgres) execOne(ctx context.Context, action, query string, args ...any) error {
	res, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if affected == 0 {
		return domain.ErrAvatarNotFound
	}

	return nil
}

// rowScanner - общий интерфейс *sql.Row и *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAvatar читает аватарку из строки со столбцами avatarColumns.
func scanAvatar(row rowScanner) (domain.Avatar, error) {
	var (
		avatar     domain.Avatar
		thumbnails []byte
	)
	err := row.Scan(
		&avatar.ID, &avatar.UserID, &avatar.FileName, &avatar.MimeType, &avatar.SizeBytes,
		&avatar.Width, &avatar.Height, &avatar.S3Key, &thumbnails, &avatar.UploadStatus,
		&avatar.ProcessingStatus, &avatar.CreatedAt, &avatar.UpdatedAt, &avatar.DeletedAt,
	)
	if err != nil {
		return domain.Avatar{}, err
	}

	if len(thumbnails) > 0 {
		if err := json.Unmarshal(thumbnails, &avatar.Thumbnails); err != nil {
			return domain.Avatar{}, fmt.Errorf("некорректные ключи миниатюр: %w", err)
		}
	}

	return avatar, nil
}

// notFoundOr превращает sql.ErrNoRows в domain.ErrAvatarNotFound, а прочие
// ошибки оборачивает с описанием действия action.
func notFoundOr(err error, action string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAvatarNotFound
	}

	return fmt.Errorf("%s: %w", action, err)
}
