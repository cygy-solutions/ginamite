package request

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxLocale "github.com/cygy-solutions/ginamite/api/context/locale"
	"github.com/cygy-solutions/ginamite/api/internal/apitest"
	commonRequest "github.com/cygy-solutions/ginamite/common/request"

	"github.com/gin-gonic/gin"
)

// pngHeader is the signature of a PNG file, enough to be detected as an image.
var pngHeader = []byte("\x89PNG\r\n\x1a\n" + "0123456789012345678901234567890123456789")

func uploadContext(t *testing.T, field, filename string, content []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("unable to create the form file: %v", err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatalf("unable to write the form file: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("unable to close the writer: %v", err)
	}

	c, recorder := apitest.NewContext("POST", "/", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	ctxLocale.Set(c, "", "en")

	return c, recorder
}

func TestGetUploadedFile(t *testing.T) {
	initLocalization(t)

	tests := []struct {
		name         string
		field        string
		content      []byte
		maxSize      int64
		checkIsImage bool
		wantFile     bool
		wantStatus   int
	}{
		{"valid image", "file", pngHeader, 1024, true, true, http.StatusOK},
		{"valid file without image check", "file", []byte("text"), 1024, false, true, http.StatusOK},
		{"missing field", "other", pngHeader, 1024, true, false, http.StatusInternalServerError},
		{"file too large", "file", pngHeader, 10, true, false, http.StatusPreconditionFailed},
		{"not an image", "file", []byte("this is only text, not an image"), 1024, true, false, http.StatusPreconditionFailed},
		{"empty file", "file", nil, 1024, true, false, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder := uploadContext(t, tt.field, "image.png", tt.content)
			file := GetUploadedFile(c, "file", tt.maxSize, tt.checkIsImage, "test", "msg")
			c.Writer.WriteHeaderNow()

			if (file != nil) != tt.wantFile {
				t.Errorf("file returned = %v, want %v", file != nil, tt.wantFile)
			}
			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
		})
	}
}

func TestGetUploadedFile_BodyTooLarge(t *testing.T) {
	initLocalization(t)

	c, recorder := uploadContext(t, "file", "image.png", bytes.Repeat(pngHeader, 10))
	c.Request.ContentLength = -1 // a chunked body, read until the limit
	commonRequest.LimitBodySize(64)(c)

	if file := GetUploadedFile(c, "file", 1024, true, "test", "msg"); file != nil {
		t.Error("a body over the limit must not return a file")
	}
	c.Writer.WriteHeaderNow()
	if recorder.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusPreconditionFailed)
	}
}

func TestSaveUploadedFile(t *testing.T) {
	initLocalization(t)

	t.Run("saved", func(t *testing.T) {
		c, _ := uploadContext(t, "file", "image.png", pngHeader)
		file := GetUploadedFile(c, "file", 1024, true, "test", "msg")
		destination := filepath.Join(t.TempDir(), "saved.png")

		if !SaveUploadedFile(c, file, destination, "test", "msg") {
			t.Fatal("SaveUploadedFile() = false, want true")
		}
		if content, err := os.ReadFile(destination); err != nil || !bytes.Equal(content, pngHeader) {
			t.Errorf("saved content = %q, %v, want the uploaded content", content, err)
		}
	})

	t.Run("existing directory mode untouched", func(t *testing.T) {
		c, _ := uploadContext(t, "file", "image.png", pngHeader)
		file := GetUploadedFile(c, "file", 1024, true, "test", "msg")
		directory := t.TempDir()
		// A mode different from the one gin applies (0o750): a directory owned by another user can not be changed.
		if err := os.Chmod(directory, 0o755); err != nil { //nolint:gosec // the test needs a readable directory
			t.Fatalf("unable to chmod the directory: %v", err)
		}

		if !SaveUploadedFile(c, file, filepath.Join(directory, "saved.png"), "test", "msg") {
			t.Fatal("SaveUploadedFile() = false, want true")
		}
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("unable to stat the directory: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("directory mode = %o, want 755", got)
		}
	})

	t.Run("missing directories created", func(t *testing.T) {
		c, _ := uploadContext(t, "file", "image.png", pngHeader)
		file := GetUploadedFile(c, "file", 1024, true, "test", "msg")
		destination := filepath.Join(t.TempDir(), "a", "b", "saved.png")

		if !SaveUploadedFile(c, file, destination, "test", "msg") {
			t.Fatal("SaveUploadedFile() = false, want true")
		}
		if _, err := os.Stat(destination); err != nil {
			t.Errorf("the file is missing: %v", err)
		}
	})

	t.Run("unable to save", func(t *testing.T) {
		c, recorder := uploadContext(t, "file", "image.png", pngHeader)
		file := GetUploadedFile(c, "file", 1024, true, "test", "msg")
		// The parent of the destination is a regular file, so the destination can not be created.
		parent := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
			t.Fatalf("unable to write the file: %v", err)
		}
		destination := filepath.Join(parent, "saved.png")

		if SaveUploadedFile(c, file, destination, "test", "msg") {
			t.Fatal("SaveUploadedFile() = true, want false")
		}
		c.Writer.WriteHeaderNow()
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	})
}

// failingReader returns some content, then an error.
type failingReader struct{ read bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("read failure")
	}
	r.read = true

	return copy(p, "partial"), nil
}

func TestWriteFile(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "saved.txt")

		if err := writeFile(strings.NewReader("content"), destination); err != nil {
			t.Fatalf("writeFile() error = %v", err)
		}
		if content, err := os.ReadFile(destination); err != nil || string(content) != "content" {
			t.Errorf("content = %q, %v, want %q", content, err, "content")
		}
	})

	t.Run("partial file removed on copy failure", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "saved.txt")

		if err := writeFile(&failingReader{}, destination); err == nil {
			t.Fatal("writeFile() error = nil, want an error")
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the partial file is still there: %v", err)
		}
	})
}
