package transfers

import (
	"bytes"
	"math"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
)

func TestJSNumber(t *testing.T) {
	ok := map[string]float64{"12": 12, " 500000 ": 500000, "1e3": 1000, "12.5": 12.5, "0x10": 16, "-3": -3}
	for in, want := range ok {
		if got, valid := jsNumber(in); !valid || got != want {
			t.Errorf("%q -> %v %v", in, got, valid)
		}
	}
	if got, valid := jsNumber("Infinity"); !valid || !math.IsInf(got, 1) {
		t.Error("Infinity")
	}
	for _, bad := range []string{"abc", "12abc", "NaN", "inf", "1_000"} {
		if _, valid := jsNumber(bad); valid {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestMinorAmount(t *testing.T) {
	v := httpx.NewV()
	minorAmount(v, "amount", 12.5)
	minorAmount(v, "amount", 0)
	minorAmount(v, "amount", 2e12)
	err := v.Err("request body").(*apperr.Error)
	issues := err.Details.([]apperr.FieldIssue)
	want := []string{
		"Expected integer, received float",
		"Number must be greater than 0",
		"Number must be less than or equal to 1000000000000",
	}
	if len(issues) != len(want) {
		t.Fatalf("issues = %+v", issues)
	}
	for i, w := range want {
		if issues[i].Message != w {
			t.Errorf("issue %d = %q, want %q", i, issues[i].Message, w)
		}
	}
}

func newMultipart(t *testing.T, build func(mw *multipart.Writer)) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	build(mw)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func TestReadMultipartDeposit(t *testing.T) {
	config.E = &config.Env{MaxUploadBytes: 1024}
	png := append([]byte{0x89, 0x50, 0x4e, 0x47}, make([]byte, 20)...)

	body, ct := newMultipart(t, func(mw *multipart.Writer) {
		_ = mw.WriteField("amount", "500000")
		_ = mw.WriteField("utr", "")
		_ = mw.WriteField("customerNote", strings.Repeat("n", 5000))
		fw, _ := mw.CreateFormFile("proof", "shot.png")
		_, _ = fw.Write(png)
	})
	r := httptest.NewRequest("POST", "/deposits", body)
	r.Header.Set("Content-Type", ct)
	fields, proof, err := readMultipartDeposit(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if fields.Amount == nil || *fields.Amount != 500000 || fields.Utr != nil {
		t.Fatalf("fields = %+v", fields)
	}
	if fields.CustomerNote == nil || len(*fields.CustomerNote) != multipartMaxFieldSize {
		t.Fatal("field value not truncated to fieldSize")
	}
	if proof == nil || proof.OriginalName != "shot.png" || !bytes.Equal(proof.Buffer, png) {
		t.Fatalf("proof = %+v", proof)
	}

	// A second file is over the files limit.
	body, ct = newMultipart(t, func(mw *multipart.Writer) {
		fw, _ := mw.CreateFormFile("proof", "a.png")
		_, _ = fw.Write(png)
		fw, _ = mw.CreateFormFile("other", "b.png")
		_, _ = fw.Write(png)
	})
	r = httptest.NewRequest("POST", "/deposits", body)
	r.Header.Set("Content-Type", ct)
	if _, _, err := readMultipartDeposit(httptest.NewRecorder(), r); !apperr.HasCode(err, apperr.PayloadTooLarge) {
		t.Errorf("two files: %v", err)
	}

	// An oversized file.
	body, ct = newMultipart(t, func(mw *multipart.Writer) {
		fw, _ := mw.CreateFormFile("proof", "a.png")
		_, _ = fw.Write(append(png, make([]byte, 2048)...))
	})
	r = httptest.NewRequest("POST", "/deposits", body)
	r.Header.Set("Content-Type", ct)
	if _, _, err := readMultipartDeposit(httptest.NewRecorder(), r); !apperr.HasCode(err, apperr.PayloadTooLarge) {
		t.Errorf("big file: %v", err)
	}

	// Too many fields.
	body, ct = newMultipart(t, func(mw *multipart.Writer) {
		for i := 0; i < 13; i++ {
			_ = mw.WriteField("f"+string(rune('a'+i)), "x")
		}
	})
	r = httptest.NewRequest("POST", "/deposits", body)
	r.Header.Set("Content-Type", ct)
	if _, _, err := readMultipartDeposit(httptest.NewRecorder(), r); !apperr.HasCode(err, apperr.PayloadTooLarge) {
		t.Errorf("13 fields: %v", err)
	}

	// A non-numeric amount is reported as NaN.
	body, ct = newMultipart(t, func(mw *multipart.Writer) { _ = mw.WriteField("amount", "abc") })
	r = httptest.NewRequest("POST", "/deposits", body)
	r.Header.Set("Content-Type", ct)
	fields, _, err = readMultipartDeposit(httptest.NewRecorder(), r)
	if err != nil || !fields.amountNaN {
		t.Fatalf("nan amount: %+v %v", fields, err)
	}
	v := httpx.NewV()
	validateDeposit(v, fields)
	e := v.Err("deposit request").(*apperr.Error)
	if e.Message != "Invalid deposit request" || e.Details.([]apperr.FieldIssue)[0].Message != "Expected number, received nan" {
		t.Fatalf("validation = %+v", e)
	}
}
