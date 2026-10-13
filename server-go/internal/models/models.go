// Package models holds every MongoDB document shape, its collection name and
// its indexes, plus the JSON views the API returns.
//
// The BSON field names, collection names and value types match the Mongoose
// schemas of the Node server exactly, so both servers read and write the same
// database.
package models

import (
	"context"
	"sync"

	"binarytrading/internal/platform/mongox"
)

var (
	indexMu sync.Mutex
	indexes []mongox.Index
)

// RegisterIndexes is called from each model file's init().
func RegisterIndexes(ix ...mongox.Index) {
	indexMu.Lock()
	defer indexMu.Unlock()
	indexes = append(indexes, ix...)
}

// EnsureIndexes creates every registered index (Mongoose autoIndex).
func EnsureIndexes(ctx context.Context) {
	indexMu.Lock()
	all := append([]mongox.Index(nil), indexes...)
	indexMu.Unlock()
	mongox.EnsureIndexes(ctx, all)
}

// Str is a helper for nullable string fields.
func Str(s string) *string { return &s }

// StrOrNil returns nil for "".
func StrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
