package mongox

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/iso"
)

// JSONValue converts a value decoded from BSON into something that serialises
// to JSON the way Mongoose lean() output does through JSON.stringify:
// ObjectIDs become hex strings and dates become ISO strings. Use it for
// schemaless (Mixed) fields such as audit before/after.
func JSONValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bson.ObjectID:
		return x.Hex()
	case bson.DateTime:
		return iso.From(x.Time())
	case time.Time:
		return iso.From(x)
	case bson.D:
		m := make(map[string]any, len(x))
		for _, e := range x {
			m[e.Key] = JSONValue(e.Value)
		}
		return m
	case bson.M:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = JSONValue(e)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = JSONValue(e)
		}
		return m
	case bson.A:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = JSONValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = JSONValue(e)
		}
		return out
	case bson.Decimal128:
		return x.String()
	default:
		return v
	}
}
