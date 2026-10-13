package transfers

import (
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	svc "binarytrading/internal/service/transfers"
)

// Limits of the Node server's @fastify/multipart registration (app.ts).
const (
	multipartMaxFiles     = 1
	multipartMaxFields    = 12
	multipartMaxFieldSize = 4096
	// The deposit route's bodyLimit headroom above MAX_UPLOAD_BYTES.
	multipartBodyHeadroom = 64 * 1024
)

// Every @fastify/multipart limit error is a 413, which the Node error handler
// reports as PAYLOAD_TOO_LARGE.
func tooLarge() error {
	return apperr.New(apperr.PayloadTooLarge, "Request body is too large", 413)
}

// Field names that would shadow Object.prototype are refused by
// @fastify/multipart (a 400, reported as VALIDATION_ERROR).
var prototypeNames = map[string]bool{
	"constructor": true, "__defineGetter__": true, "__defineSetter__": true, "hasOwnProperty": true,
	"__lookupGetter__": true, "__lookupSetter__": true, "isPrototypeOf": true, "propertyIsEnumerable": true,
	"toString": true, "valueOf": true, "__proto__": true, "toLocaleString": true,
}

// readMultipartDeposit reads a deposit submitted as multipart/form-data.
//
// The screenshot is buffered rather than streamed to disk, so a request
// failing validation leaves nothing behind and the file type can be checked
// from its own bytes before anything is written.
func readMultipartDeposit(w http.ResponseWriter, r *http.Request) (depositFields, *svc.ProofFile, error) {
	// The JSON body limit middleware skips multipart; the upload cap applies here.
	r.Body = http.MaxBytesReader(w, r.Body, config.E.MaxUploadBytes+multipartBodyHeadroom)
	mr, err := r.MultipartReader()
	if err != nil {
		return depositFields{}, nil, apperr.New(apperr.ValidationError, err.Error(), 400)
	}

	fields := map[string]string{}
	var proofFile *svc.ProofFile
	files, fieldCount := 0, 0

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return depositFields{}, nil, tooLarge()
			}
			// busboy's "Unexpected end of multipart data" ends parsing quietly.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return depositFields{}, nil, apperr.New(apperr.ValidationError, err.Error(), 400)
		}

		_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename, isFile := params["filename"]
		name := part.FormName()

		if isFile {
			files++
			if files > multipartMaxFiles {
				return depositFields{}, nil, tooLarge()
			}
			buf, err := io.ReadAll(io.LimitReader(part, config.E.MaxUploadBytes+1))
			if err != nil {
				return depositFields{}, nil, readErr(err)
			}
			if int64(len(buf)) > config.E.MaxUploadBytes {
				return depositFields{}, nil, tooLarge()
			}
			// Other file fields are drained and ignored.
			if name == "proof" && len(buf) > 0 {
				if filename == "" {
					filename = "screenshot"
				}
				proofFile = &svc.ProofFile{Buffer: buf, OriginalName: filename}
			}
			continue
		}

		fieldCount++
		if fieldCount > multipartMaxFields {
			return depositFields{}, nil, tooLarge()
		}
		if prototypeNames[name] {
			return depositFields{}, nil, apperr.New(apperr.ValidationError, "prototype property is not allowed as field name", 400)
		}
		// Values longer than fieldSize are truncated, not rejected (busboy).
		value, err := io.ReadAll(io.LimitReader(part, multipartMaxFieldSize))
		if err != nil {
			return depositFields{}, nil, readErr(err)
		}
		if _, err := io.Copy(io.Discard, part); err != nil {
			return depositFields{}, nil, readErr(err)
		}
		fields[name] = string(value)
	}

	var out depositFields
	if s := fields["amount"]; s != "" {
		if f, ok := jsNumber(s); ok {
			out.Amount = &f
		} else {
			out.amountNaN = true
		}
	}
	out.PaymentMethodKey = nonEmpty(fields["paymentMethodKey"])
	out.Utr = nonEmpty(fields["utr"])
	out.CustomerNote = nonEmpty(fields["customerNote"])
	return out, proofFile, nil
}

func readErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return tooLarge()
	}
	return err
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// jsNumber is JavaScript's Number(string) for the inputs a form can carry;
// ok is false for NaN.
func jsNumber(s string) (float64, bool) {
	t := strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
	if t == "" {
		return 0, true
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(lower, "0b") {
		n, err := strconv.ParseInt(t[2:], map[byte]int{'x': 16, 'o': 8, 'b': 2}[lower[1]], 64)
		if err != nil {
			return 0, false
		}
		return float64(n), true
	}
	// ParseFloat would also accept "inf", "nan" and underscores; Number() does not.
	if strings.ContainsAny(lower, "_n") || strings.Contains(lower, "inf") {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// ── Zod-equivalent checks ───────────────────────────────────────────────

// jsLen is String.prototype.length, which is what Zod's min/max count.
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

func str(v *httpx.V, path, s string, min, max int) {
	n := jsLen(s)
	if n < min {
		v.Add(path, fmt.Sprintf("String must contain at least %d character(s)", min))
	}
	if max > 0 && n > max {
		v.Add(path, fmt.Sprintf("String must contain at most %d character(s)", max))
	}
}

func optStr(v *httpx.V, path string, s *string, min, max int) {
	if s != nil {
		str(v, path, *s, min, max)
	}
}

// minorAmount is minorAmountSchema: z.number().int().positive().max(1e12).
func minorAmount(v *httpx.V, path string, f float64) {
	if math.IsNaN(f) {
		v.Add(path, "Expected number, received nan")
		return
	}
	v.IsInt(path, f)
	v.Positive(path, f)
	if f > 1_000_000_000_000 {
		v.Add(path, "Number must be less than or equal to 1000000000000")
	}
}
