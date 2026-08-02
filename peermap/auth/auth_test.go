package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sigcn/pg/secure/aescbc"
)

func TestSecretRoundTrip(t *testing.T) {
	authenticator := NewAuthenticator("test-key")
	want := Net{ID: "network", Alias: "alias", Neighbors: []string{"neighbor"}}

	encoded, err := authenticator.GenerateSecretAdmin(true, want, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := authenticator.ParseSecret(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != want.ID || got.Alias != want.Alias || !got.Admin {
		t.Fatalf("unexpected secret: %+v", got)
	}
	if len(got.Neighbors) != 1 || got.Neighbors[0] != want.Neighbors[0] {
		t.Fatalf("unexpected neighbors: %v", got.Neighbors)
	}
}

func TestSecretRejectsTampering(t *testing.T) {
	authenticator := NewAuthenticator("test-key")
	encoded, err := authenticator.GenerateSecret(Net{ID: "network"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	token[len(token)-1] ^= 1

	if _, err := authenticator.ParseSecret(base64.URLEncoding.EncodeToString(token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("got %v, want %v", err, ErrInvalidToken)
	}
}

func TestSecretRejectsLegacyCBC(t *testing.T) {
	const key = "test-key"
	plain, err := json.Marshal(JSONSecret{Network: "network", Deadline: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key))
	legacy, err := aescbc.Encrypt(sum[:], plain)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewAuthenticator(key).ParseSecret(base64.URLEncoding.EncodeToString(legacy)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("got %v, want %v", err, ErrInvalidToken)
	}
}

func TestSecretRejectsExpiredToken(t *testing.T) {
	authenticator := NewAuthenticator("test-key")
	encoded, err := authenticator.GenerateSecret(Net{ID: "network"}, -time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := authenticator.ParseSecret(encoded); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want %v", err, ErrTokenExpired)
	}
}
