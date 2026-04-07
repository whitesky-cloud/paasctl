package clients

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTokenNeedsRefreshForExpiredJWT(t *testing.T) {
	if !tokenNeedsRefresh(testJWT(time.Now().Add(-time.Hour).Unix())) {
		t.Fatalf("tokenNeedsRefresh() = false, want true for expired token")
	}
}

func TestTokenDoesNotNeedRefreshForValidJWT(t *testing.T) {
	if tokenNeedsRefresh(testJWT(time.Now().Add(24 * time.Hour).Unix())) {
		t.Fatalf("tokenNeedsRefresh() = true, want false for valid token")
	}
}

func TestParseRefreshedWhiteSkyJWTFromPlainString(t *testing.T) {
	want := testJWT(time.Now().Add(24 * time.Hour).Unix())
	got, err := parseRefreshedWhiteSkyJWT([]byte(want))
	if err != nil {
		t.Fatalf("parseRefreshedWhiteSkyJWT() error = %v", err)
	}
	if got != want {
		t.Fatalf("parseRefreshedWhiteSkyJWT() = %q, want %q", got, want)
	}
}

func TestParseRefreshedWhiteSkyJWTFromJSONPayload(t *testing.T) {
	want := testJWT(time.Now().Add(24 * time.Hour).Unix())
	got, err := parseRefreshedWhiteSkyJWT([]byte(`{"jwt":"` + want + `"}`))
	if err != nil {
		t.Fatalf("parseRefreshedWhiteSkyJWT() error = %v", err)
	}
	if got != want {
		t.Fatalf("parseRefreshedWhiteSkyJWT() = %q, want %q", got, want)
	}
}

func testJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + strconv.FormatInt(exp, 10) + `}`))
	return strings.Join([]string{header, payload, "sig"}, ".")
}
