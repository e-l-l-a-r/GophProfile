-- Миграция: метаданные аватарок.
-- Сами файлы лежат в S3: s3_key - ключ оригинала, thumbnail_s3_keys - ключи
-- миниатюр по размеру ({"100x100": "...", "300x300": "..."}).
-- Удаление мягкое: deleted_at заполняется, файлы удаляет воркер асинхронно.
CREATE TABLE IF NOT EXISTS avatars (
    id                UUID PRIMARY KEY,
    user_id           VARCHAR(255) NOT NULL,
    file_name         VARCHAR(255) NOT NULL,
    mime_type         VARCHAR(100) NOT NULL,
    size_bytes        BIGINT       NOT NULL CHECK (size_bytes >= 0),
    width             INTEGER      NOT NULL DEFAULT 0 CHECK (width >= 0),
    height            INTEGER      NOT NULL DEFAULT 0 CHECK (height >= 0),
    s3_key            VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB,
    upload_status     VARCHAR(50)  NOT NULL DEFAULT 'uploading'
        CHECK (upload_status IN ('uploading', 'uploaded', 'failed')),
    processing_status VARCHAR(50)  NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'processing', 'completed', 'failed')),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

-- Индекс для выборки неудалённых аватарок пользователя от новых к старым.
CREATE INDEX IF NOT EXISTS idx_avatars_user_id
    ON avatars (user_id, created_at DESC) WHERE deleted_at IS NULL;

-- Индекс для поиска аватарок по состоянию загрузки и обработки.
CREATE INDEX IF NOT EXISTS idx_avatars_status
    ON avatars (upload_status, processing_status);

-- Миграция: идентификаторы обработанных воркером сообщений брокера.
-- Позволяет не выполнять повторно операцию при повторной доставке сообщения.
CREATE TABLE IF NOT EXISTS processed_messages (
    message_id   VARCHAR(255) PRIMARY KEY,
    processed_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
