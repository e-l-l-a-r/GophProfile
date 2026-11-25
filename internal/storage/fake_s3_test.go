package storage

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeObject - объект поддельного хранилища.
type fakeObject struct {
	contentType string
	data        []byte
}

// fakeS3 - поддельное S3-хранилище поверх httptest: поддерживает ровно те
// запросы, которые выполняет S3 (HEAD/PUT бакета, PUT/HEAD/GET/DELETE объекта).
type fakeS3 struct {
	objects map[string]fakeObject
	buckets map[string]bool
	// fail - HTTP-статус, которым отвечать на все запросы; 0 - работать штатно.
	fail int
	mu   sync.Mutex
}

// newFakeS3 запускает поддельное хранилище и возвращает клиент S3 к нему.
func newFakeS3(t *testing.T, bucket string) (*S3, *fakeS3) {
	t.Helper()

	fake := &fakeS3{objects: map[string]fakeObject{}, buckets: map[string]bool{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	s, err := NewS3(Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "access",
		SecretKey: "secret",
		Bucket:    bucket,
		// без повторов: тесты сбоев не должны ждать задержек клиента
		MaxRetries: 1,
	})
	require.NoError(t, err)

	return s, fake
}

func (f *fakeS3) setFail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = status
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail != 0 {
		writeS3Error(w, r, f.fail, "InternalError")
		return
	}

	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if key == "" {
		f.serveBucket(w, r, bucket)
		return
	}
	if !f.buckets[bucket] {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchBucket")
		return
	}
	f.serveObject(w, r, bucket+"/"+key)
}

func (f *fakeS3) serveBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodHead:
		if !f.buckets[bucket] {
			writeS3Error(w, r, http.StatusNotFound, "NoSuchBucket")
			return
		}
	case http.MethodPut:
		f.buckets[bucket] = true
	default:
		writeS3Error(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *fakeS3) serveObject(w http.ResponseWriter, r *http.Request, path string) {
	switch r.Method {
	case http.MethodPut:
		data, err := readPayload(r)
		if err != nil {
			writeS3Error(w, r, http.StatusBadRequest, "IncompleteBody")
			return
		}
		f.objects[path] = fakeObject{contentType: r.Header.Get("Content-Type"), data: data}
		w.Header().Set("ETag", `"etag-`+path+`"`)
	case http.MethodHead, http.MethodGet:
		obj, ok := f.objects[path]
		if !ok {
			writeS3Error(w, r, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", obj.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
		w.Header().Set("ETag", `"etag-`+path+`"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2026 15:04:05 GMT")
		if r.Method == http.MethodGet {
			_, _ = w.Write(obj.data)
		}
	case http.MethodDelete:
		// Как и S3, удаление отсутствующего объекта считается успешным.
		delete(f.objects, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeS3Error(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

// readPayload читает тело PUT-запроса, разбирая потоковую подпись aws-chunked
// ("<hex-размер>;chunk-signature=...\r\n<данные>\r\n").
func readPayload(r *http.Request) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return io.ReadAll(r.Body)
	}

	var out bytes.Buffer
	br := bufio.NewReader(r.Body)
	for {
		header, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeHex, _, _ := strings.Cut(strings.TrimSpace(header), ";")
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return out.Bytes(), nil
		}
		if _, err := io.CopyN(&out, br, size); err != nil {
			return nil, err
		}
		if _, err := br.Discard(2); err != nil { // \r\n после данных
			return nil, err
		}
	}
}

// writeS3Error отвечает ошибкой в формате S3. У HEAD-ответа тела нет.
func writeS3Error(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
	}
}
