// Package users is the /users HTTP API (modules/users/user.routes.ts).
package users

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/audit"
	svc "binarytrading/internal/service/users"
)

// Mount registers the routes under /users.
func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	r.With(httpx.RateLimit(httpx.RateLimits.Read)).Get("/me", httpx.H(getMe))
	r.With(httpx.RateLimit(httpx.RateLimits.Read)).Patch("/me", httpx.H(patchMe))
	r.With(httpx.RateLimit(httpx.RateLimits.Read)).Get("/me/favourites", httpx.H(getFavourites))
	r.With(httpx.RateLimit(httpx.RateLimits.Read)).Put("/me/favourites", httpx.H(putFavourites))
}

type favouritesBody struct {
	AssetIDs []string `json:"assetIds"`
}

func getFavourites(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	ids, err := svc.GetFavourites(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, favouritesBody{AssetIDs: ids})
}

// parseFavourites: {assetIds: string[]} of 24-hex ids, de-duplicated in
// order, at most svc.MaxFavourites.
func parseFavourites(r *http.Request) ([]string, error) {
	fields := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &fields); err != nil {
		return nil, err
	}
	v := httpx.NewV()
	raw, ok := fields["assetIds"]
	if !ok {
		v.Add("assetIds", "Required")
		return nil, v.Err("request body")
	}
	var items []json.RawMessage
	if kind := jsonKind(raw); kind != "array" || json.Unmarshal(raw, &items) != nil {
		v.Add("assetIds", "Expected array, received "+jsonKind(raw))
		return nil, v.Err("request body")
	}
	ids := make([]string, len(items))
	for i, item := range items {
		if jsonKind(item) != "string" || json.Unmarshal(item, &ids[i]) != nil {
			v.Add("assetIds."+strconv.Itoa(i), "Expected string, received "+jsonKind(item))
		}
	}
	out, invalid := svc.NormaliseFavouriteIDs(ids)
	for _, i := range invalid {
		if jsonKind(items[i]) == "string" {
			v.Add("assetIds."+strconv.Itoa(i), "Invalid asset id")
		}
	}
	v.Check(len(out) <= svc.MaxFavourites, "assetIds", "Array must contain at most "+strconv.Itoa(svc.MaxFavourites)+" element(s)")
	if err := v.Err("request body"); err != nil {
		return nil, err
	}
	return out, nil
}

func putFavourites(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	ids, err := parseFavourites(r)
	if err != nil {
		return err
	}
	saved, err := svc.SetFavourites(r.Context(), actor.UserID, ids)
	if err != nil {
		return err
	}
	return httpx.OK(w, favouritesBody{AssetIDs: saved})
}

// Nickname. Restricted so it cannot be dressed up to look like someone else
// or like a system label.
var nicknameRe = regexp.MustCompile(`^[A-Za-z0-9_.#-]+$`)

// parseUpdateProfile mirrors updateProfileSchema, including the order in
// which Zod applies checks and transforms.
func parseUpdateProfile(r *http.Request, now time.Time) (svc.UpdateProfileInput, error) {
	f, err := decodeFields(r)
	if err != nil {
		return svc.UpdateProfileInput{}, err
	}
	var in svc.UpdateProfileInput
	if s, ok := f.str("fullName", false); ok {
		f.min("fullName", s, 2)
		f.max("fullName", s, 120)
		t := strings.TrimSpace(s)
		in.FullName = &t
	}
	if s, ok := f.str("username", false); ok {
		f.min("username", s, 3)
		f.max("username", s, 32)
		t := strings.TrimSpace(s)
		f.regex("username", t, nicknameRe, "Use letters, numbers and . _ - # only")
		in.Username = &t
	}
	if s, ok := f.str("phone", false); ok {
		f.min("phone", s, 6)
		f.max("phone", s, 20)
		t := strings.TrimSpace(s)
		in.Phone = &t
	}
	if s, ok := f.str("country", false); ok {
		f.length("country", s, 2)
		u := strings.ToUpper(s)
		in.Country = &u
	}
	// An empty string clears the stored date; anything else must be a real
	// calendar day. The shape alone is not enough - "2026-02-31" matches the
	// pattern and is not a date, and a birthday cannot be in the future.
	if raw, present := f.m["dateOfBirth"]; present {
		if jsonKind(raw) != "string" {
			// z.union reports a type mismatch on every branch as one issue.
			f.v.Add("dateOfBirth", "Invalid input")
		} else {
			s, _ := f.str("dateOfBirth", false)
			if s != "" && !util.IsPlausibleBirthDate(s, now) {
				f.v.Add("dateOfBirth", "Enter a real date of birth")
			}
			in.DateOfBirth = &s
		}
	}
	if s, ok := f.str("address", false); ok {
		f.max("address", s, 300)
		t := strings.TrimSpace(s)
		in.Address = &t
	}
	return in, f.err()
}

func getMe(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	user, err := svc.GetUser(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, user)
}

func patchMe(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	in, err := parseUpdateProfile(r, time.Now())
	if err != nil {
		return err
	}
	before, err := svc.GetUser(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	after, err := svc.UpdateProfile(r.Context(), actor.UserID, in)
	if err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.UserUpdated
	e.Resource = "User"
	e.ResourceID = actor.UserID
	e.Before = map[string]any{
		"fullName": before.FullName, "username": before.Username, "phone": before.Phone, "country": before.Country,
	}
	e.After = map[string]any{
		"fullName": after.FullName, "username": after.Username, "phone": after.Phone, "country": after.Country,
	}
	audit.Log(e)

	return httpx.OK(w, after)
}
