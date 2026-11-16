// Package domain содержит модель предметной области сервиса аватарок: аватарку,
// статусы её загрузки и обработки и ошибки, общие для всех слоёв.
package domain

import (
	"errors"
	"slices"
	"time"
)

var (
	// ErrAvatarNotFound возвращается, если аватарки нет или она удалена.
	ErrAvatarNotFound = errors.New("аватарка не найдена")
	// ErrForbidden возвращается при попытке изменить чужую аватарку.
	ErrForbidden = errors.New("действие с чужой аватаркой запрещено")
	// ErrInvalidImage возвращается для файла, который не является допустимым
	// изображением (формат, разрешение).
	ErrInvalidImage = errors.New("недопустимое изображение")
	// ErrInvalidParameter возвращается для некорректного параметра запроса
	// (размер, формат).
	ErrInvalidParameter = errors.New("некорректный параметр")
)

// OriginalKey возвращает ключ оригинала аватарки в хранилище файлов;
// ext - расширение без точки.
func OriginalKey(avatarID, ext string) string {
	return "originals/" + avatarID + "." + ext
}

// ThumbnailKey возвращает ключ миниатюры размера size ("100x100") в хранилище
// файлов. Ключ детерминирован, поэтому повторное создание миниатюры
// перезаписывает тот же объект.
func ThumbnailKey(avatarID, size, ext string) string {
	return "thumbnails/" + avatarID + "/" + size + "." + ext
}

// Keys возвращает ключи всех файлов аватарки: оригинала и миниатюр.
func (a Avatar) Keys() []string {
	keys := []string{a.S3Key}
	for _, key := range a.Thumbnails {
		keys = append(keys, key)
	}
	slices.Sort(keys[1:])

	return keys
}

// UploadStatus - состояние загрузки оригинала аватарки в хранилище файлов.
type UploadStatus string

const (
	// UploadStatusUploading - запись создана, оригинал ещё загружается.
	UploadStatusUploading UploadStatus = "uploading"
	// UploadStatusUploaded - оригинал сохранён в хранилище файлов.
	UploadStatusUploaded UploadStatus = "uploaded"
	// UploadStatusFailed - оригинал сохранить не удалось.
	UploadStatusFailed UploadStatus = "failed"
)

// ProcessingStatus - состояние асинхронной обработки аватарки (создания миниатюр).
type ProcessingStatus string

const (
	// ProcessingStatusPending - обработка ещё не начиналась.
	ProcessingStatusPending ProcessingStatus = "pending"
	// ProcessingStatusProcessing - воркер создаёт миниатюры.
	ProcessingStatusProcessing ProcessingStatus = "processing"
	// ProcessingStatusCompleted - миниатюры созданы.
	ProcessingStatusCompleted ProcessingStatus = "completed"
	// ProcessingStatusFailed - миниатюры создать не удалось.
	ProcessingStatusFailed ProcessingStatus = "failed"
)

// Avatar - метаданные аватарки. Сами файлы (оригинал и миниатюры) лежат в
// хранилище файлов по ключам S3Key и Thumbnails.
type Avatar struct {
	// CreatedAt - время создания; заполняется хранилищем.
	CreatedAt time.Time
	// UpdatedAt - время последнего изменения; заполняется хранилищем.
	UpdatedAt time.Time
	// DeletedAt - время мягкого удаления; nil у неудалённой аватарки.
	DeletedAt *time.Time
	// Thumbnails - ключи миниатюр в хранилище файлов по размеру ("100x100").
	Thumbnails map[string]string
	// ID - идентификатор аватарки (UUID).
	ID string
	// UserID - идентификатор владельца.
	UserID string
	// FileName - исходное имя загруженного файла.
	FileName string
	// MimeType - MIME-тип оригинала: image/jpeg, image/png или image/webp.
	MimeType string
	// S3Key - ключ оригинала в хранилище файлов.
	S3Key string
	// UploadStatus - состояние загрузки оригинала.
	UploadStatus UploadStatus
	// ProcessingStatus - состояние создания миниатюр.
	ProcessingStatus ProcessingStatus
	// SizeBytes - размер оригинала в байтах.
	SizeBytes int64
	// Width - ширина оригинала в пикселях.
	Width int
	// Height - высота оригинала в пикселях.
	Height int
}
