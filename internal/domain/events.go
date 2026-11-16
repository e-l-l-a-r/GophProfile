package domain

const (
	// RoutingKeyAvatarUploaded - ключ маршрутизации события загрузки аватарки.
	RoutingKeyAvatarUploaded = "avatar.uploaded"
	// RoutingKeyAvatarDeleted - ключ маршрутизации события удаления аватарки.
	RoutingKeyAvatarDeleted = "avatar.deleted"
)

// AvatarUploadEvent - событие загрузки оригинала аватарки: воркер создаёт по
// нему миниатюры.
type AvatarUploadEvent struct {
	// AvatarID - идентификатор аватарки.
	AvatarID string `json:"avatar_id"`
	// UserID - идентификатор владельца.
	UserID string `json:"user_id"`
	// S3Key - ключ оригинала в хранилище файлов.
	S3Key string `json:"s3_key"`
}

// AvatarDeleteEvent - событие мягкого удаления аватарки: воркер удаляет по
// нему файлы из хранилища.
type AvatarDeleteEvent struct {
	// AvatarID - идентификатор аватарки.
	AvatarID string `json:"avatar_id"`
	// S3Keys - ключи оригинала и миниатюр в хранилище файлов.
	S3Keys []string `json:"s3_keys"`
}
