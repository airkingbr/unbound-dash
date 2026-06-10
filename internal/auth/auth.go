// Package auth implements a minimal single-user session authentication
// scheme: a configured admin password protects a login endpoint, which
// issues an HMAC-signed, time-limited session cookie.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// CookieName is the name of the session cookie.
	CookieName = "unbound_dash_session"
	// SessionTTL is how long a session remains valid.
	SessionTTL = 12 * time.Hour
)

// CheckPassword returns true if the supplied password matches the
// configured admin password, using a constant-time comparison.
func CheckPassword(configured, supplied string) bool {
	if configured == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(supplied)) == 1
}

// NewSessionToken returns a signed token encoding an expiry time.
func NewSessionToken(secret string) string {
	expiry := time.Now().Add(SessionTTL).Unix()
	return signToken(secret, expiry)
}

func signToken(secret string, expiry int64) string {
	payload := strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ValidateSessionToken checks the signature and expiry of a session token.
func ValidateSessionToken(secret, token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}

	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}

	expected := signToken(secret, expiry)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return false
	}

	return time.Now().Unix() < expiry
}

// SetSessionCookie attaches a session cookie to the response.
func SetSessionCookie(w http.ResponseWriter, secret string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    NewSessionToken(secret),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionTTL.Seconds()),
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// Middleware rejects requests without a valid session cookie.
func Middleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(CookieName)
			if err != nil || !ValidateSessionToken(secret, cookie.Value) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
