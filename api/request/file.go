package request

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	ctxLocale "github.com/cygy-solutions/ginamite/api/context/locale"
	"github.com/cygy-solutions/ginamite/api/response"
	"github.com/cygy-solutions/ginamite/common/log"

	"github.com/gin-gonic/gin"
)

// GetUploadedFile returns the uploaded file, or nil after sending the error response: the file is missing, larger than
// maxSize or than the limit of the request body (see request.LimitBodySize), or not an image while checkIsImage is
// true.
func GetUploadedFile(c *gin.Context, fieldName string, maxSize int64, checkIsImage bool, logContext, errorMessageKey string) *multipart.FileHeader {
	locale := ctxLocale.Get(c)

	file, err := c.FormFile(fieldName)
	if maxBytesErr := (*http.MaxBytesError)(nil); errors.As(err, &maxBytesErr) {
		log.Error("max body size exceeded", "context", logContext, "limit", maxBytesErr.Limit)
		response.MaxFileSizeExceeded(c, locale, errorMessageKey, uint64(min(maxSize, maxBytesErr.Limit))) //nolint:gosec // both limits are positive
		return nil
	}
	if err != nil {
		log.Error("file not found",
			"context", logContext,
			"error", err.Error(),
		)
		response.FileNotFound(c, locale, errorMessageKey)
		return nil
	}

	if file.Size > maxSize {
		log.Error("max file size exceeded", "context", logContext)
		response.MaxFileSizeExceeded(c, locale, errorMessageKey, uint64(maxSize)) //nolint:gosec // maxSize is a positive limit set by the code
		return nil
	}

	if checkIsImage {
		f, err := file.Open()
		if err != nil {
			log.Error("unable to open the file",
				"context", logContext,
				"error", err.Error(),
			)
			response.UnableToReadFile(c, locale, errorMessageKey)
			return nil
		}

		defer func() {
			if err := f.Close(); err != nil {
				log.Error("unable to close the uploaded file", "error", err)
			}
		}()

		buff := make([]byte, 512) // docs tell that it take only first 512 bytes into consideration
		if _, err = f.Read(buff); err != nil {
			log.Error("unable to read the file",
				"context", logContext,
				"error", err.Error(),
			)
			response.UnableToReadFile(c, locale, errorMessageKey)
			return nil
		}

		mimeType := http.DetectContentType(buff)
		if !strings.HasPrefix(mimeType, "image/") {
			log.Error("the file is not an image",
				"context", logContext,
				"error", "invalid mime-type",
				"mime-type", mimeType,
			)
			response.InvalidMimeType(c, locale, errorMessageKey)
			return nil
		}
	}

	return file
}

// SaveUploadedFile saves the uploaded file to the destination, creating the missing parent directories. Unlike
// gin.Context.SaveUploadedFile, it never changes the mode of an existing directory: the chmod of a directory owned by
// another user (the system temporary directory for instance) fails with "operation not permitted".
func SaveUploadedFile(c *gin.Context, file *multipart.FileHeader, destination, logContext, errorMessageKey string) bool {
	if err := saveFile(file, destination); err != nil {
		log.Error("unable to save the file",
			"context", logContext,
			"error", err.Error(),
		)

		locale := ctxLocale.Get(c)
		response.UnableToSaveFile(c, locale, errorMessageKey)

		return false
	}

	return true
}

// saveFile copies the uploaded file to the destination.
func saveFile(file *multipart.FileHeader, destination string) error {
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("unable to open the uploaded file: %w", err)
	}
	defer func() { _ = src.Close() }() // read-only file, nothing to report on close

	return writeFile(src, destination)
}

// writeFile writes the content of src to the destination, creating the missing parent directories. The destination is
// removed when the content can not be fully written, so that no partial file is left on disk.
func writeFile(src io.Reader, destination string) (err error) {
	if err = os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("unable to create the directory: %w", err)
	}

	out, err := os.Create(destination) //nolint:gosec // the destination is built by the caller, not by the client
	if err != nil {
		return fmt.Errorf("unable to create the file: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("unable to close the file: %w", closeErr)
		}
		if err != nil {
			_ = os.Remove(destination) // best effort, the original error is the one to report
		}
	}()

	if _, err = io.Copy(out, src); err != nil {
		return fmt.Errorf("unable to copy the file: %w", err)
	}

	return nil
}
