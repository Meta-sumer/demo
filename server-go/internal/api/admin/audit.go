package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/service/audit"
)

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// auditDoc turns an API view into the object Mongoose stores in an audit
// row's before/after (a Mixed path): the view's JSON field names in their
// order, with the Date fields back as BSON dates and whole numbers as
// integers.
func auditDoc(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	out, err := decodeValue(dec)
	if err != nil {
		return nil
	}
	return out
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			doc := bson.D{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				doc = append(doc, bson.E{Key: kt.(string), Value: val})
			}
			_, err := dec.Token() // }
			return doc, err
		}
		arr := bson.A{}
		for dec.More() {
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		_, err := dec.Token() // ]
		return arr, err
	case json.Number:
		if !strings.ContainsAny(t.String(), ".eE") {
			if n, err := t.Int64(); err == nil {
				return n, nil
			}
		}
		f, _ := t.Float64()
		return f, nil
	case string:
		if isoDateRe.MatchString(t) {
			if tm, err := time.Parse(iso.Layout, t); err == nil {
				return tm.UTC(), nil
			}
		}
		return t, nil
	}
	return tok, nil // bool or nil
}

// entry starts an audit row for the calling admin.
func entry(r *http.Request, actor *httpx.Actor, action, resource, resourceID string) audit.Entry {
	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action, e.Resource, e.ResourceID = action, resource, resourceID
	return e
}
