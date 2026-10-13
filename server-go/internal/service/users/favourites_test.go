package users

import (
	"slices"
	"testing"
)

func TestNormaliseFavouriteIDs(t *testing.T) {
	a, b, c := "aaaaaaaaaaaaaaaaaaaaaaaa", "BBBBBBBBBBBBBBBBBBBBBBBB", "cccccccccccccccccccccccc"
	out, invalid := NormaliseFavouriteIDs([]string{b, a, "nope", b, c, "bbbbbbbbbbbbbbbbbbbbbbbb", a + "0"})
	want := []string{"bbbbbbbbbbbbbbbbbbbbbbbb", a, c}
	if !slices.Equal(out, want) {
		t.Fatalf("got %v, want %v (first occurrence kept, order kept, case folded)", out, want)
	}
	if !slices.Equal(invalid, []int{2, 6}) {
		t.Fatalf("invalid = %v", invalid)
	}
	empty, bad := NormaliseFavouriteIDs(nil)
	if empty == nil || len(empty) != 0 || len(bad) != 0 {
		t.Fatal("empty input must give an empty (non-nil) list")
	}
}
