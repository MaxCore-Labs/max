package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
)

const (
	totpLockoutThreshold = 5
	totpLockoutDuration  = 5 * time.Minute
)

// GenerateTOTPCode computes a 6-digit RFC 6238 TOTP code for base32Secret at timestamp at.
func GenerateTOTPCode(base32Secret string, at time.Time) (string, error) {
	clean := strings.ToUpper(strings.TrimRight(strings.TrimSpace(base32Secret), "="))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("invalid base32 secret: %w", err)
	}
	counter := uint64(at.Unix() / 30)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	otp := binCode % 1_000_000
	return fmt.Sprintf("%06d", otp), nil
}

// VerifyTOTPCode checks code against base32Secret within a ±1 step (30s) window in constant time.
func VerifyTOTPCode(base32Secret, code string, now time.Time) bool {
	trimmed := strings.TrimSpace(code)
	if len(trimmed) != 6 {
		return false
	}
	for _, skew := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		want, err := GenerateTOTPCode(base32Secret, now.Add(skew))
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(trimmed)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) SetupTotp(ctx context.Context, _ gen.SetupTotpRequestObject) (gen.SetupTotpResponseObject, error) {
	u, ok := currentUser(ctx)
	if !ok {
		return gen.SetupTotp401JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "unauthenticated"}}, nil
	}
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	stored, err := s.sealSecret([]byte(secret))
	if err != nil {
		return nil, err
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.SetOwnerTOTPPending(ctx, u.ID, stored, nowMs)
	}); err != nil {
		return nil, err
	}
	otpURL := fmt.Sprintf("otpauth://totp/Max:%s?secret=%s&issuer=Max", url.PathEscape(u.Username), secret)
	return gen.SetupTotp200JSONResponse{
		Secret:     secret,
		OtpauthUrl: otpURL,
	}, nil
}

func (s *Server) ConfirmTotp(ctx context.Context, req gen.ConfirmTotpRequestObject) (gen.ConfirmTotpResponseObject, error) {
	u, ok := currentUser(ctx)
	if !ok {
		return gen.ConfirmTotp401JSONResponse{Error: "unauthenticated"}, nil
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Code) == "" {
		return gen.ConfirmTotp400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing code"}}, nil
	}
	totpRec, found, err := s.deps.Store.Read().GetOwnerTOTP(ctx)
	if err != nil || !found {
		return nil, errors.New("owner not found")
	}
	now := s.deps.Clock.Now()
	nowMs := now.UnixMilli()
	if totpRec.LockedUntil != nil && nowMs < *totpRec.LockedUntil {
		return gen.ConfirmTotp429JSONResponse{Error: "too many failed TOTP attempts; temporarily locked"}, nil
	}
	if len(totpRec.TOTPPending) == 0 {
		return gen.ConfirmTotp400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "no pending TOTP setup"}}, nil
	}
	plainPending, err := s.openSecret(totpRec.TOTPPending)
	if err != nil {
		return nil, err
	}
	if !VerifyTOTPCode(string(plainPending), req.Body.Code, now) {
		s.recordTOTPFailure(ctx, totpRec, now)
		return gen.ConfirmTotp400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid TOTP code"}}, nil
	}
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.ConfirmOwnerTOTP(ctx, u.ID, totpRec.TOTPPending, nowMs)
	}); err != nil {
		return nil, err
	}
	_ = s.appendAudit(ctx, "totp.enable", u.ID, map[string]bool{"enabled": true})
	return gen.ConfirmTotp200JSONResponse{
		Id:          u.ID,
		Username:    u.Username,
		TotpEnabled: true,
	}, nil
}

func (s *Server) verifyLoginTOTP(ctx context.Context, totpCode *string) (gen.LoginResponseObject, bool) {
	totpRec, found, err := s.deps.Store.Read().GetOwnerTOTP(ctx)
	if err != nil || !found {
		return nil, false
	}
	now := s.deps.Clock.Now()
	nowMs := now.UnixMilli()
	if totpRec.LockedUntil != nil && nowMs < *totpRec.LockedUntil {
		return gen.Login429JSONResponse{Error: "TOTP locked temporarily due to repeated failed attempts"}, true
	}
	if !totpRec.TOTPEnabled {
		return nil, false
	}
	code := ""
	if totpCode != nil {
		code = strings.TrimSpace(*totpCode)
	}
	if code == "" {
		return gen.Login401JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "totp_required", Code: ptrString("totp_required")},
		}, true
	}
	plainSecret, err := s.openSecret(totpRec.TOTPSecret)
	if err != nil || !VerifyTOTPCode(string(plainSecret), code, now) {
		s.recordTOTPFailure(ctx, totpRec, now)
		return gen.Login401JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "invalid TOTP code"},
		}, true
	}
	if totpRec.FailedAttempts > 0 || totpRec.LockedUntil != nil {
		_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
			return tx.ResetOwnerTOTPFailures(ctx, totpRec.UserID, nowMs)
		})
	}
	return nil, false
}

func (s *Server) recordTOTPFailure(ctx context.Context, rec app.TOTPRecord, now time.Time) {
	next := rec.FailedAttempts + 1
	var lockPtr *int64
	if next >= totpLockoutThreshold {
		until := now.Add(totpLockoutDuration).UnixMilli()
		lockPtr = &until
	}
	_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return tx.RecordOwnerTOTPFailure(ctx, rec.UserID, next, lockPtr, now.UnixMilli())
	})
}

func (s *Server) sealSecret(plain []byte) ([]byte, error) {
	if s.deps.Connections != nil {
		return s.deps.Connections.EncryptBytes(plain)
	}
	return plain, nil
}

func (s *Server) openSecret(ct []byte) ([]byte, error) {
	if s.deps.Connections != nil {
		return s.deps.Connections.DecryptBytes(ct)
	}
	return ct, nil
}

func (s *Server) computeStatusWarnings(ctx context.Context) []string {
	var out []string
	if !isLoopbackOrTailnetURL(s.deps.Config.PublicURL) {
		owner, ok, _ := s.deps.Store.Read().GetOwnerUser(ctx)
		if !ok || !owner.TOTPEnabled {
			out = append(out, "Bắt buộc bật TOTP khi PublicURL nằm ngoài loopback hoặc mạng tailnet.")
		}
	}
	return out
}

func isLoopbackOrTailnetURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		a := addr.Unmap()
		if a.IsLoopback() {
			return true
		}
		if a.Is4() {
			b := a.As4()
			if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
				return true
			}
		}
	}
	return false
}

func ptrString(s string) *string { return &s }
