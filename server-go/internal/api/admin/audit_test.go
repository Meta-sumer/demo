package admin

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/iso"
)

func TestAuditDoc(t *testing.T) {
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	v := struct {
		ID      string    `json:"id"`
		Amount  int64     `json:"amount"`
		Ratio   float64   `json:"ratio"`
		At      iso.Time  `json:"at"`
		Missing *string   `json:"missing"`
		List    []float64 `json:"list"`
	}{"x", 1500, 0.5, iso.From(at), nil, []float64{1, 2.5}}

	doc, ok := auditDoc(v).(bson.D)
	if !ok {
		t.Fatalf("want bson.D, got %T", auditDoc(v))
	}
	keys := []string{"id", "amount", "ratio", "at", "missing", "list"}
	for i, k := range keys {
		if doc[i].Key != k {
			t.Fatalf("key %d = %s, want %s", i, doc[i].Key, k)
		}
	}
	if doc[1].Value != int64(1500) || doc[2].Value != 0.5 || doc[4].Value != nil {
		t.Fatalf("numbers/null not restored: %#v", doc)
	}
	if got, ok := doc[3].Value.(time.Time); !ok || !got.Equal(at) {
		t.Fatalf("date not restored: %#v", doc[3].Value)
	}
	if list := doc[5].Value.(bson.A); list[0] != int64(1) || list[1] != 2.5 {
		t.Fatalf("list = %#v", list)
	}
	if auditDoc(nil) != nil {
		t.Fatal("nil should stay nil")
	}
}
