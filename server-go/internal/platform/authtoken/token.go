// Package authtoken signs and verifies JWTs. The claim set, issuer and
// algorithm (HS256) match the Node server, so a token issued by either server
// is accepted by the other.
package authtoken

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/util"
)

const Issuer = "binary-trading"

type AccessClaims struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	SID         string   `json:"sid"`
	Typ         string   `json:"typ"`
	// Ver is the user's session version when the token was issued. Revoking a
	// user's sessions moves the stored version past it, so the token stops
	// working at once rather than at expiry (see CheckSession). Absent on
	// tokens issued before versions existed, which read as version 0.
	Ver *int64 `json:"ver,omitempty"`
	jwt.RegisteredClaims
}

type RefreshClaims struct {
	SID string `json:"sid"`
	FID string `json:"fid"`
	Typ string `json:"typ"`
	jwt.RegisteredClaims
}

func AccessTTLMs() int64 {
	ms, _ := util.ParseDuration(config.E.JWTAccessTTL)
	return ms
}

func RefreshTTLMs() int64 {
	ms, _ := util.ParseDuration(config.E.JWTRefreshTTL)
	return ms
}

func registered(userID string, ttlMs int64) jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Issuer:    Issuer,
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(ttlMs/1000) * time.Second)),
	}
}

func SignAccess(userID, role string, permissions []string, sessionID string) (string, error) {
	if permissions == nil {
		permissions = []string{}
	}
	claims := AccessClaims{
		Role: role, Permissions: permissions, SID: sessionID, Typ: "access",
		RegisteredClaims: registered(userID, AccessTTLMs()),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.E.JWTSecret))
}

// SignAccessVersioned signs an access token bound to the user's current
// session version, so a later revocation invalidates it immediately.
func SignAccessVersioned(userID, role string, permissions []string, sessionID string, version int64) (string, error) {
	if permissions == nil {
		permissions = []string{}
	}
	v := version
	claims := AccessClaims{
		Role: role, Permissions: permissions, SID: sessionID, Typ: "access", Ver: &v,
		RegisteredClaims: registered(userID, AccessTTLMs()),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.E.JWTSecret))
}

func SignRefresh(userID, sessionID, familyID string) (string, error) {
	claims := RefreshClaims{
		SID: sessionID, FID: familyID, Typ: "refresh",
		RegisteredClaims: registered(userID, RefreshTTLMs()),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.E.JWTRefreshSecret))
}

func VerifyAccess(token string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	if err := verify(token, config.E.JWTSecret, claims); err != nil {
		return nil, err
	}
	if claims.Typ != "access" {
		return nil, apperr.New(apperr.TokenInvalid, "Wrong token type", 401)
	}
	return claims, nil
}

func VerifyRefresh(token string) (*RefreshClaims, error) {
	claims := &RefreshClaims{}
	if err := verify(token, config.E.JWTRefreshSecret, claims); err != nil {
		return nil, err
	}
	if claims.Typ != "refresh" {
		return nil, apperr.New(apperr.TokenInvalid, "Wrong token type", 401)
	}
	return claims, nil
}

func verify(token, secret string, claims jwt.Claims) error {
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithIssuer(Issuer), jwt.WithValidMethods([]string{"HS256"}))
	if err == nil {
		return nil
	}
	if errors.Is(err, jwt.ErrTokenExpired) {
		return apperr.New(apperr.TokenExpired, "Token has expired", 401)
	}
	return apperr.New(apperr.TokenInvalid, "Token is not valid", 401)
}
