// Package storage содержит хранилище файлов аватарок в S3-совместимом
// хранилище (MinIO, AWS S3).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// region - регион бакета. Задаётся явно, чтобы клиент не запрашивал
// расположение бакета перед каждой операцией.
const region = "us-east-1"

// ErrObjectNotFound возвращается, если объекта с таким ключом нет.
var ErrObjectNotFound = errors.New("объект не найден")

// Config - параметры подключения к хранилищу.
type Config struct {
	// Endpoint - адрес хранилища без схемы (host:port).
	Endpoint string
	// AccessKey - идентификатор ключа доступа.
	AccessKey string
	// SecretKey - секретный ключ доступа.
	SecretKey string
	// Bucket - имя бакета.
	Bucket string
	// MaxRetries - число попыток запроса при временных сбоях; 0 - значение
	// по умолчанию клиента minio.
	MaxRetries int
	// UseSSL включает HTTPS.
	UseSSL bool
}

// Object - объект хранилища, открытый для чтения. Body нужно закрыть.
type Object struct {
	// Body - содержимое объекта.
	Body io.ReadCloser
	// ContentType - MIME-тип, сохранённый при загрузке.
	ContentType string
	// ETag - метка версии содержимого.
	ETag string
	// Size - размер в байтах.
	Size int64
}

// S3 - хранилище файлов в одном бакете S3-совместимого хранилища.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 создаёт клиент хранилища. Подключение устанавливается лениво: для
// проверки доступности вызовите Ping, для создания бакета - EnsureBucket.
func NewS3(cfg Config) (*S3, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:      credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:     cfg.UseSSL,
		Region:     region,
		MaxRetries: cfg.MaxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("не удалось создать клиент S3: %w", err)
	}

	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// EnsureBucket создаёт бакет, если его ещё нет.
func (s *S3) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("не удалось проверить бакет %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}

	err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region})
	if err != nil && !isCode(err, "BucketAlreadyOwnedByYou") {
		return fmt.Errorf("не удалось создать бакет %q: %w", s.bucket, err)
	}

	return nil
}

// Ping проверяет доступность хранилища и наличие бакета.
func (s *S3) Ping(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("хранилище файлов недоступно: %w", err)
	}
	if !exists {
		return fmt.Errorf("бакет %q не существует", s.bucket)
	}

	return nil
}

// Put сохраняет объект с ключом key размером size байт и MIME-типом
// contentType. Существующий объект с тем же ключом перезаписывается.
func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("не удалось сохранить объект %q: %w", key, err)
	}

	return nil
}

// Get открывает объект с ключом key для чтения. Возвращает ErrObjectNotFound,
// если объекта нет. Ответ S3 на HEAD-запрос не позволяет отличить
// отсутствие бакета от отсутствия объекта, поэтому наличие бакета
// обеспечивается заранее вызовом EnsureBucket.
func (s *S3) Get(ctx context.Context, key string) (Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return Object{}, s.wrapGetError(key, err)
	}

	// GetObject ленив: запрос выполняется при первом обращении, поэтому
	// отсутствие объекта обнаруживается здесь.
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return Object{}, s.wrapGetError(key, err)
	}

	return Object{Body: obj, ContentType: info.ContentType, ETag: info.ETag, Size: info.Size}, nil
}

// Download читает объект с ключом key целиком. Возвращает ErrObjectNotFound,
// если объекта нет.
func (s *S3) Download(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Body.Close() }()

	data, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать объект %q: %w", key, err)
	}

	return data, nil
}

// Delete удаляет объекты с ключами keys. Отсутствие объекта ошибкой не
// считается, поэтому повторное удаление безопасно.
func (s *S3) Delete(ctx context.Context, keys ...string) error {
	var errs []error
	for _, key := range keys {
		if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("не удалось удалить объект %q: %w", key, err))
		}
	}

	return errors.Join(errs...)
}

// wrapGetError превращает ответ «объект не найден» в ErrObjectNotFound, а
// прочие ошибки оборачивает.
func (s *S3) wrapGetError(key string, err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %q", ErrObjectNotFound, key)
	}

	return fmt.Errorf("не удалось получить объект %q: %w", key, err)
}

// isNotFound сообщает, что err - ответ хранилища об отсутствии объекта.
// Ответ на HEAD-запрос не содержит тела с кодом, поэтому учитывается и статус
// 404; отсутствие бакета отсутствием объекта не считается.
func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchBucket" {
		return false
	}

	return resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound
}

// isCode сообщает, что err - ответ хранилища с кодом ошибки code.
func isCode(err error, code string) bool {
	return minio.ToErrorResponse(err).Code == code
}
