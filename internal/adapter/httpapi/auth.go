package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
)

const (
	sessionCookieName = "max_session"
	sessionDuration   = 30 * 24 * time.Hour
	minPasswordLength = 12

	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
)

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySessionHash
	ctxKeyCookies
)

type pendingCookies struct {
	list []*http.Cookie
}

func addResponseCookie(ctx context.Context, c *http.Cookie) {
	if pc, ok := ctx.Value(ctxKeyCookies).(*pendingCookies); ok && pc != nil {
		pc.list = append(pc.list, c)
	}
}

func currentUser(ctx context.Context) (app.UserRecord, bool) {
	u, ok := ctx.Value(ctxKeyUser).(app.UserRecord)
	return u, ok
}

// HashPassword hashes a plaintext password (>= 12 chars) using Argon2id.
func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < minPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	dk := argon2.IDKey([]byte(password), salt[:], argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt[:]),
		base64.RawStdEncoding.EncodeToString(dk),
	), nil
}

// VerifyPassword checks plaintext password against an Argon2id encoded hash in constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem, timeIter uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &timeIter, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	wantKey, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(wantKey) == 0 {
		return false
	}
	gotKey := argon2.IDKey([]byte(password), salt, timeIter, mem, threads, uint32(len(wantKey)))
	return subtle.ConstantTimeCompare(gotKey, wantKey) == 1
}

// SetAdminPassword creates or updates the single owner user's Argon2id password hash (used by `max admin set-password`).
func SetAdminPassword(ctx context.Context, store app.Store, clock app.Clock, ids app.IDGen, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	nowMs := clock.Now().UnixMilli()
	return store.WithTx(ctx, func(tx app.Tx) error {
		existing, ok, err := tx.GetOwnerUser(ctx)
		if err != nil {
			return err
		}
		uid := ids.New()
		createdAt := nowMs
		username := "owner"
		if ok {
			uid = existing.ID
			createdAt = existing.CreatedAt
			username = existing.Username
		}
		return tx.UpsertOwnerUser(ctx, app.UserRecord{
			ID:           uid,
			Username:     username,
			PasswordHash: hash,
			TOTPEnabled:  existing.TOTPEnabled,
			CreatedAt:    createdAt,
			UpdatedAt:    nowMs,
		})
	})
}

func hashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func generateSessionToken() (raw, tokenHash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b[:])
	return raw, hashSessionToken(raw), nil
}

func (s *Server) Healthz(_ context.Context, _ gen.HealthzRequestObject) (gen.HealthzResponseObject, error) {
	return gen.Healthz200JSONResponse{Ok: true}, nil
}

func (s *Server) Login(ctx context.Context, req gen.LoginRequestObject) (gen.LoginResponseObject, error) {
	owner, ok, err := s.deps.Store.Read().GetOwnerUser(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.Login503JSONResponse{
			Error: "Owner account is not initialized; run `max admin set-password` or set MAX_ADMIN_PASSWORD.",
		}, nil
	}
	if req.Body == nil || !VerifyPassword(owner.PasswordHash, req.Body.Password) {
		return gen.Login401JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid credentials"},
		}, nil
	}

	rawTok, tokHash, err := generateSessionToken()
	if err != nil {
		return nil, err
	}
	now := s.deps.Clock.Now()
	exp := now.Add(sessionDuration)
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertAuthSession(ctx, app.AuthSessionRecord{
			TokenHash: tokHash,
			UserID:    owner.ID,
			ExpiresAt: exp.UnixMilli(),
			CreatedAt: now.UnixMilli(),
		})
	}); err != nil {
		return nil, err
	}

	addResponseCookie(ctx, &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawTok,
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteStrictMode,
	})
	return gen.Login200JSONResponse{
		Id:          owner.ID,
		Username:    owner.Username,
		TotpEnabled: owner.TOTPEnabled,
	}, nil
}

func (s *Server) Logout(ctx context.Context, _ gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	if h, ok := ctx.Value(ctxKeySessionHash).(string); ok && h != "" {
		_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
			return tx.DeleteAuthSession(ctx, h)
		})
	}
	addResponseCookie(ctx, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteStrictMode,
	})
	return gen.Logout204Response{}, nil
}

func (s *Server) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	u, ok := currentUser(ctx)
	if !ok {
		return nil, errors.New("unauthenticated")
	}
	return gen.GetMe200JSONResponse{
		Id:          u.ID,
		Username:    u.Username,
		TotpEnabled: u.TOTPEnabled,
	}, nil
}

func (s *Server) ChangePassword(ctx context.Context, req gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error) {
	u, ok := currentUser(ctx)
	if !ok || req.Body == nil {
		return gen.ChangePassword401JSONResponse{Error: "unauthenticated"}, nil
	}
	if !VerifyPassword(u.PasswordHash, req.Body.CurrentPassword) {
		return gen.ChangePassword401JSONResponse{Error: "incorrect current password"}, nil
	}
	if err := SetAdminPassword(ctx, s.deps.Store, s.deps.Clock, s.deps.IDs, req.Body.NewPassword); err != nil {
		return gen.ChangePassword400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()},
		}, nil
	}
	return gen.ChangePassword204Response{}, nil
}
