//go:build integration

// Package e2e проверяет сервис целиком: HTTP-интерфейс, сервис, воркер,
// PostgreSQL, MinIO и RabbitMQ в контейнерах testcontainers.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/e-l-l-a-r/gophprofile/internal/broker"
	"github.com/e-l-l-a-r/gophprofile/internal/domain"
	"github.com/e-l-l-a-r/gophprofile/internal/handler"
	"github.com/e-l-l-a-r/gophprofile/internal/imageproc"
	"github.com/e-l-l-a-r/gophprofile/internal/logger"
	"github.com/e-l-l-a-r/gophprofile/internal/repository"
	"github.com/e-l-l-a-r/gophprofile/internal/retry"
	"github.com/e-l-l-a-r/gophprofile/internal/service"
	"github.com/e-l-l-a-r/gophprofile/internal/storage"
	"github.com/e-l-l-a-r/gophprofile/internal/worker"
	"github.com/e-l-l-a-r/gophprofile/web"
)

const (
	bucket   = "avatars"
	maxSize  = 10 << 20
	waitTime = 20 * time.Second
	tick     = 100 * time.Millisecond
)

// E2ESuite поднимает инфраструктуру в контейнерах, HTTP-сервер и воркер.
type E2ESuite struct {
	suite.Suite
	pgC     *tcpostgres.PostgresContainer
	minioC  *tcminio.MinioContainer
	rabbitC *tcrabbit.RabbitMQContainer
	repo    *repository.Postgres
	files   *storage.S3
	mq      *broker.Client
	server  *httptest.Server
	stop    context.CancelFunc
	done    chan error
	amqpURL string
}

func TestE2E(t *testing.T) {
	suite.Run(t, new(E2ESuite))
}

func (s *E2ESuite) SetupSuite() {
	ctx := context.Background()
	log := logger.Nop()
	var err error

	s.pgC, err = tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gophprofile"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies())
	s.Require().NoError(err)
	s.minioC, err = tcminio.Run(ctx, "pgsty/minio:RELEASE.2026-08-04T00-00-00Z",
		tcminio.WithUsername("minioadmin"), tcminio.WithPassword("minioadmin"))
	s.Require().NoError(err)
	s.rabbitC, err = tcrabbit.Run(ctx, "rabbitmq:4-management-alpine")
	s.Require().NoError(err)

	dsn, err := s.pgC.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)
	s.repo, err = repository.NewPostgres(dsn)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Migrate())

	endpoint, err := s.minioC.ConnectionString(ctx)
	s.Require().NoError(err)
	s.files, err = storage.NewS3(storage.Config{
		Endpoint: endpoint, AccessKey: s.minioC.Username, SecretKey: s.minioC.Password, Bucket: bucket,
	})
	s.Require().NoError(err)
	s.Require().NoError(s.files.EnsureBucket(ctx))

	s.amqpURL, err = s.rabbitC.AmqpURL(ctx)
	s.Require().NoError(err)
	s.mq, err = broker.Dial(s.amqpURL, log)
	s.Require().NoError(err)

	avatars := service.NewAvatarService(s.repo, s.files, s.mq, log)
	h := handler.New(avatars, handler.Web{Static: web.Static, Templates: web.Templates}, handler.Config{
		Checkers:      map[string]handler.Checker{"database": s.repo, "storage": s.files, "broker": s.mq},
		MaxUploadSize: maxSize,
		HealthTimeout: 2 * time.Second,
	}, log)
	s.server = httptest.NewServer(h.Routes())

	policy := retry.Policy{Initial: 10 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 2, MaxAttempts: 3}
	w := worker.New(s.repo, s.files, policy, 2, log)
	workerCtx, stop := context.WithCancel(ctx)
	s.stop = stop
	s.done = make(chan error, 1)
	go func() { s.done <- w.Run(workerCtx, s.mq) }()
}

func (s *E2ESuite) TearDownSuite() {
	if s.stop != nil {
		s.stop()
		select {
		case err := <-s.done:
			s.NoError(err)
		case <-time.After(waitTime):
			s.Fail("воркер не остановился")
		}
	}
	if s.server != nil {
		s.server.Close()
	}
	if s.mq != nil {
		s.NoError(s.mq.Close())
	}
	if s.repo != nil {
		s.NoError(s.repo.Close())
	}
	for _, c := range []testcontainers.Container{s.pgC, s.minioC, s.rabbitC} {
		testcontainers.CleanupContainer(s.T(), c)
	}
}

// pngImage возвращает PNG-изображение width x height.
func pngImage(width, height int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)

	return buf.Bytes()
}

// request выполняет запрос и возвращает ответ с прочитанным телом.
func (s *E2ESuite) request(method, path string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.server.URL+path, body)
	s.Require().NoError(err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	s.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)

	return resp, data
}

// upload загружает изображение data от имени userID.
func (s *E2ESuite) upload(userID string, data []byte) (*http.Response, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "avatar.png")
	s.Require().NoError(err)
	_, err = fw.Write(data)
	s.Require().NoError(err)
	s.Require().NoError(mw.Close())

	return s.request(http.MethodPost, "/api/v1/avatars", &buf,
		map[string]string{"Content-Type": mw.FormDataContentType(), handler.HeaderUserID: userID})
}

// metadata - часть ответа с метаданными, которую проверяет тест.
type metadata struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
	Thumbnails       []struct {
		Size string `json:"size"`
		URL  string `json:"url"`
	} `json:"thumbnails"`
	Dimensions struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"dimensions"`
}

// metadataOf возвращает метаданные аватарки id и статус ответа.
func (s *E2ESuite) metadataOf(id string) (metadata, int) {
	resp, body := s.request(http.MethodGet, "/api/v1/avatars/"+id+"/metadata", nil, nil)
	var m metadata
	if resp.StatusCode == http.StatusOK {
		s.Require().NoError(json.Unmarshal(body, &m))
	}

	return m, resp.StatusCode
}

// queueLen возвращает число сообщений в очереди queue.
func (s *E2ESuite) queueLen(queue string) int {
	conn, err := amqp.Dial(s.amqpURL)
	s.Require().NoError(err)
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	s.Require().NoError(err)

	q, err := ch.QueueInspect(queue)
	s.Require().NoError(err)

	return q.Messages
}

func (s *E2ESuite) TestHealth() {
	resp, body := s.request(http.MethodGet, "/health", nil, nil)
	s.Equal(http.StatusOK, resp.StatusCode, string(body))
	s.JSONEq(`{"status":"ok","components":{"database":{"status":"ok"},"storage":{"status":"ok"},"broker":{"status":"ok"}}}`,
		string(body))
}

func (s *E2ESuite) TestAvatarLifecycle() {
	resp, body := s.upload("alice", pngImage(640, 480))
	s.Require().Equal(http.StatusCreated, resp.StatusCode, string(body))
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	s.Require().NoError(json.Unmarshal(body, &created))
	s.Equal("processing", created.Status)
	id := created.ID

	// Воркер создаёт миниатюры асинхронно.
	s.Require().Eventually(func() bool {
		m, code := s.metadataOf(id)
		return code == http.StatusOK && m.ProcessingStatus == "completed" && len(m.Thumbnails) == 2
	}, waitTime, tick, "миниатюры не созданы")

	m, _ := s.metadataOf(id)
	s.Equal(640, m.Dimensions.Width)
	s.Equal(480, m.Dimensions.Height)

	for _, size := range imageproc.ThumbnailSizes {
		key := domain.ThumbnailKey(id, size.Name, "png")
		data, err := s.files.Download(context.Background(), key)
		s.Require().NoError(err, "миниатюра %s лежит в хранилище", size.Name)
		info, err := imageproc.Inspect(data)
		s.Require().NoError(err)
		s.Equal(size.Width, info.Width)
	}

	resp, body = s.request(http.MethodGet, "/api/v1/avatars/"+id+"?size=100x100&format=webp", nil, nil)
	s.Require().Equal(http.StatusOK, resp.StatusCode)
	s.Equal("image/webp", resp.Header.Get("Content-Type"))
	info, err := imageproc.Inspect(body)
	s.Require().NoError(err)
	s.Equal(imageproc.Info{Format: imageproc.FormatWebP, Width: 100, Height: 100}, info)

	etag := resp.Header.Get("ETag")
	resp, _ = s.request(http.MethodGet, "/api/v1/avatars/"+id+"?size=100x100&format=webp", nil,
		map[string]string{"If-None-Match": etag})
	s.Equal(http.StatusNotModified, resp.StatusCode)

	resp, _ = s.request(http.MethodGet, "/api/v1/users/alice/avatar", nil, nil)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Equal("image/png", resp.Header.Get("Content-Type"))

	resp, _ = s.request(http.MethodDelete, "/api/v1/avatars/"+id, nil, map[string]string{handler.HeaderUserID: "mallory"})
	s.Equal(http.StatusForbidden, resp.StatusCode)

	resp, _ = s.request(http.MethodDelete, "/api/v1/avatars/"+id, nil, map[string]string{handler.HeaderUserID: "alice"})
	s.Require().Equal(http.StatusNoContent, resp.StatusCode)

	_, code := s.metadataOf(id)
	s.Equal(http.StatusNotFound, code)

	// Воркер удаляет файлы асинхронно.
	keys := []string{domain.OriginalKey(id, "png"), domain.ThumbnailKey(id, "100x100", "png"), domain.ThumbnailKey(id, "300x300", "png")}
	s.Require().Eventually(func() bool {
		for _, key := range keys {
			if _, err := s.files.Download(context.Background(), key); err == nil {
				return false
			}
		}
		return true
	}, waitTime, tick, "файлы не удалены из хранилища")
}

func (s *E2ESuite) TestUploadValidation() {
	resp, body := s.upload("alice", []byte("definitely not an image"))
	s.Equal(http.StatusBadRequest, resp.StatusCode)
	s.JSONEq(`{"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}`, string(body))

	resp, body = s.upload("alice", bytes.Repeat([]byte{0}, maxSize+1))
	s.Equal(http.StatusRequestEntityTooLarge, resp.StatusCode)
	s.JSONEq(fmt.Sprintf(`{"error":"File too large","max_size":%d}`, maxSize), string(body))
}

func (s *E2ESuite) TestWebUploadAndGallery() {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	s.Require().NoError(mw.WriteField("userId", "bob"))
	fw, err := mw.CreateFormFile("image", "bob.png")
	s.Require().NoError(err)
	_, err = fw.Write(pngImage(50, 50))
	s.Require().NoError(err)
	s.Require().NoError(mw.Close())

	resp, _ := s.request(http.MethodPost, "/web/upload", &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	s.Require().Equal(http.StatusSeeOther, resp.StatusCode)
	s.Equal("/web/gallery/bob", resp.Header.Get("Location"))

	resp, body := s.request(http.MethodGet, "/web/gallery/bob", nil, nil)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Contains(string(body), "bob.png")
	s.Contains(string(body), "1 avatar(s)")
}

func (s *E2ESuite) TestBrokenMessageGoesToDeadLetterQueue() {
	// Событие с телом, которое воркер не может разобрать, обрабатывать
	// повторно бессмысленно: оно уходит в очередь недоставленных.
	before := s.queueLen(broker.DeadLetterQueue(broker.QueueDeleted))
	s.Require().NoError(s.mq.Publish(context.Background(), domain.RoutingKeyAvatarDeleted, "broken"))

	s.Require().Eventually(func() bool {
		return s.queueLen(broker.DeadLetterQueue(broker.QueueDeleted)) == before+1
	}, waitTime, tick)
}
