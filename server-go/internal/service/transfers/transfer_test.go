package transfers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
)

func code(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	return ""
}

func TestDetectFileType(t *testing.T) {
	pad := func(b ...byte) []byte { return append(b, make([]byte, 16)...) }
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 8)...)
	cases := []struct {
		buf  []byte
		mime string
		ext  string
	}{
		{pad(0xff, 0xd8, 0xff), "image/jpeg", "jpg"},
		{pad(0x89, 0x50, 0x4e, 0x47), "image/png", "png"},
		{webp, "image/webp", "webp"},
		{append([]byte("%PDF-1.7"), make([]byte, 8)...), "application/pdf", "pdf"},
	}
	for _, c := range cases {
		m, e, err := DetectFileType(c.buf)
		if err != nil || m != c.mime || e != c.ext {
			t.Errorf("%s: got %s %s %v", c.mime, m, e, err)
		}
	}
	if _, _, err := DetectFileType([]byte("MZ this is an executable, not a screenshot")); code(err) != apperr.ValidationError {
		t.Errorf("executable accepted: %v", err)
	}
	if _, _, err := DetectFileType([]byte{0xff, 0xd8, 0xff}); err == nil || err.(*apperr.Error).Message != "That file is too small to be a payment screenshot" {
		t.Errorf("tiny file: %v", err)
	}
}

func TestStoreAndResolveAttachment(t *testing.T) {
	dir := t.TempDir()
	config.E = &config.Env{UploadDir: dir, MaxUploadBytes: 64}
	png := append([]byte{0x89, 0x50, 0x4e, 0x47}, make([]byte, 20)...)
	stored, err := StoreAttachment(t.Context(), png)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MimeType != "image/png" || stored.Size != int64(len(png)) || filepath.Ext(stored.FileID) != ".png" {
		t.Fatalf("stored = %+v", stored)
	}
	p, err := ResolveAttachmentPath(stored.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAttachmentPath("../escape.png"); code(err) != apperr.ValidationError {
		t.Errorf("path escape allowed: %v", err)
	}
	if _, err := ResolveAttachmentPath("missing.png"); code(err) != apperr.NotFound {
		t.Errorf("missing file: %v", err)
	}
	DeleteAttachment(stored.FileID)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("attachment not deleted")
	}

	if _, err := StoreAttachment(t.Context(), nil); code(err) != apperr.ValidationError {
		t.Errorf("empty upload: %v", err)
	}
	big := append([]byte{0x89, 0x50, 0x4e, 0x47}, make([]byte, 100)...)
	if _, err := StoreAttachment(t.Context(), big); code(err) != apperr.PayloadTooLarge {
		t.Errorf("oversized upload: %v", err)
	}
}

func TestNormaliseUtr(t *testing.T) {
	s := func(v string) *string { return &v }
	got, err := NormaliseUtr(s("  abc123def456 "))
	if err != nil || *got != "ABC123DEF456" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, blank := range []*string{nil, s(""), s("   ")} {
		if got, err := NormaliseUtr(blank); got != nil || err != nil {
			t.Errorf("blank: %v %v", got, err)
		}
	}
	for _, bad := range []string{"abc", "drop table; --", strings.Repeat("1", 41)} {
		if _, err := NormaliseUtr(s(bad)); code(err) != apperr.ValidationError {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateDestination(t *testing.T) {
	s := func(v string) *string { return &v }
	upi, err := ValidateDestination(models.PayoutDestination{Type: models.PaymentMethodUPI, UpiID: s(" someone@okbank "), AccountName: s("ignored")})
	if err != nil || *upi.UpiID != "someone@okbank" || upi.AccountName != nil {
		t.Fatalf("upi: %+v %v", upi, err)
	}
	if _, err := ValidateDestination(models.PayoutDestination{Type: models.PaymentMethodUPI, UpiID: s("not-an-upi")}); code(err) != apperr.ValidationError {
		t.Error("bad UPI accepted")
	}
	bank, err := ValidateDestination(models.PayoutDestination{
		Type: models.PaymentMethodBank, AccountName: s(" Asha "), AccountNumber: s("123456789"), Ifsc: s("hdfc0001234"),
	})
	if err != nil || *bank.Ifsc != "HDFC0001234" || *bank.AccountName != "Asha" || bank.BankName != nil || bank.UpiID != nil {
		t.Fatalf("bank: %+v %v", bank, err)
	}
	bad := []models.PayoutDestination{
		{Type: models.PaymentMethodBank, AccountName: s("A"), AccountNumber: s("123456"), Ifsc: s("HDFC0001234")},
		{Type: models.PaymentMethodBank, AccountName: s("Asha"), AccountNumber: s("12ab56"), Ifsc: s("HDFC0001234")},
		{Type: models.PaymentMethodBank, AccountName: s("Asha"), AccountNumber: s("123456"), Ifsc: s("HDFC1001234")},
	}
	for i, d := range bad {
		if _, err := ValidateDestination(d); code(err) != apperr.ValidationError {
			t.Errorf("bad bank %d accepted", i)
		}
	}
}

func TestSanitiseFilename(t *testing.T) {
	cases := map[string]string{
		"C:\\Users\\me\\pay (1).png": "pay 1.png",
		"../../etc/passwd":           "passwd",
		"日本.jpg":                     ".jpg",
		"///":                        "attachment",
	}
	for in, want := range cases {
		if got := SanitiseFilename(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestValidatePaymentMethod(t *testing.T) {
	s := func(v string) *string { return &v }
	n := func(v int64) *int64 { return &v }
	if err := validatePaymentMethod(PaymentMethodInput{Type: models.PaymentMethodUPI, UpiID: s("pay@bank")}); err != nil {
		t.Fatal(err)
	}
	if err := validatePaymentMethod(PaymentMethodInput{Type: models.PaymentMethodUPI}); code(err) != apperr.ValidationError {
		t.Error("missing UPI accepted")
	}
	if err := validatePaymentMethod(PaymentMethodInput{Type: models.PaymentMethodBank, AccountName: s("X"), AccountNumber: s("123456"), Ifsc: s("sbin0123456")}); err != nil {
		t.Fatal(err)
	}
	err := validatePaymentMethod(PaymentMethodInput{Type: models.PaymentMethodUPI, UpiID: s("pay@bank"), MinAmount: n(10), MaxAmount: n(5)})
	if err == nil || err.(*apperr.Error).Message != "The minimum amount cannot exceed the maximum" {
		t.Errorf("min > max: %v", err)
	}
}
