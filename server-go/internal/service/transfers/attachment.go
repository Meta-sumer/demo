package transfers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
)

// Payment-proof storage.
//
// Screenshots are written to a directory on disk and served only through an
// authenticated route - never from a public static mount, because a payment
// screenshot contains a customer's bank details. The stored name is a
// generated id, so a caller can never influence the path, and the extension is
// chosen from the sniffed type rather than from the uploaded name.

type magic struct {
	mime      string
	extension string
	test      func(b []byte) bool
}

var magicBytes = []magic{
	{"image/jpeg", "jpg", func(b []byte) bool { return b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff }},
	{"image/png", "png", func(b []byte) bool { return b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4e && b[3] == 0x47 }},
	{"image/webp", "webp", func(b []byte) bool { return string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" }},
	{"application/pdf", "pdf", func(b []byte) bool { return bytes.HasPrefix(b, []byte("%PDF-")) }},
}

// StoredFile describes an attachment written to disk.
type StoredFile struct {
	FileID   string
	MimeType string
	Size     int64
	SHA256   string
}

func uploadRoot() string {
	root, err := filepath.Abs(config.E.UploadDir)
	if err != nil {
		return filepath.Clean(config.E.UploadDir)
	}
	return root
}

// DetectFileType identifies the file from its own bytes. A client-declared
// content type is not evidence; a renamed executable would claim image/png
// just as readily as a screenshot does.
func DetectFileType(buf []byte) (mime, extension string, err error) {
	if len(buf) < 12 {
		return "", "", apperr.Validation("That file is too small to be a payment screenshot", nil)
	}
	for _, m := range magicBytes {
		if m.test(buf) {
			return m.mime, m.extension, nil
		}
	}
	return "", "", apperr.Validation("Upload a JPG, PNG, WebP image or a PDF", nil)
}

// StoreAttachment validates and writes an upload under UPLOAD_DIR.
func StoreAttachment(_ context.Context, buf []byte) (StoredFile, error) {
	if len(buf) == 0 {
		return StoredFile{}, apperr.Validation("The uploaded file is empty", nil)
	}
	if int64(len(buf)) > config.E.MaxUploadBytes {
		return StoredFile{}, apperr.New(apperr.PayloadTooLarge,
			"Attachments must be under "+strconv.FormatInt(config.E.MaxUploadBytes/1024/1024, 10)+" MB", 413)
	}
	mime, ext, err := DetectFileType(buf)
	if err != nil {
		return StoredFile{}, err
	}
	fileID := uuid.NewString() + "." + ext

	root := uploadRoot()
	if err := os.MkdirAll(root, 0o777); err != nil {
		return StoredFile{}, err
	}
	f, err := os.OpenFile(filepath.Join(root, fileID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return StoredFile{}, err
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return StoredFile{}, err
	}
	if err := f.Close(); err != nil {
		return StoredFile{}, err
	}
	sum := sha256.Sum256(buf)
	return StoredFile{FileID: fileID, MimeType: mime, Size: int64(len(buf)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// ResolveAttachmentPath maps a stored id to a path, refusing anything that
// escapes the upload root.
func ResolveAttachmentPath(fileID string) (string, error) {
	root := uploadRoot()
	var resolved string
	// path.resolve semantics: an absolute id replaces the root.
	if filepath.IsAbs(fileID) {
		resolved = filepath.Clean(fileID)
	} else {
		resolved = filepath.Join(root, fileID)
	}
	if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", apperr.Validation("Invalid attachment reference", nil)
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", apperr.NotFoundErr(apperr.NotFound, "Attachment not found")
	}
	return resolved, nil
}

// OpenAttachment opens a stored file for streaming; the caller closes it.
func OpenAttachment(fileID string) (*os.File, error) {
	p, err := ResolveAttachmentPath(fileID)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// AttachmentSize returns the stored file's size in bytes.
func AttachmentSize(fileID string) (int64, error) {
	p, err := ResolveAttachmentPath(fileID)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// DeleteAttachment removes a stored file; failures are logged, not returned.
func DeleteAttachment(fileID string) {
	p, err := ResolveAttachmentPath(fileID)
	if err == nil {
		err = os.Remove(p)
	}
	if err != nil {
		logx.Warn("could not delete attachment", "err", err.Error(), "fileId", fileID)
	}
}
