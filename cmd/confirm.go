package cmd

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const confirmTTL = 15 * time.Minute

type confirmPayload struct {
	Action    string         `json:"action"`
	Detail    map[string]any `json:"detail"`
	ExpiresAt int64          `json:"expires_at"`
}

type writeOptions struct {
	DryRun  bool
	Confirm string
}

func parseWriteArgs(args []string) (writeOptions, []string) {
	var opts writeOptions
	clean := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			opts.DryRun = true
		case a == "--confirm":
			if i+1 >= len(args) {
				fail(exitUsage, "E_USAGE", "--confirm requires a token", nil, false)
			}
			opts.Confirm = args[i+1]
			i++
		case strings.HasPrefix(a, "--confirm="):
			opts.Confirm = strings.TrimPrefix(a, "--confirm=")
		default:
			clean = append(clean, a)
		}
	}
	return opts, clean
}

func handleWriteGate(opts writeOptions, action string, detail map[string]any) bool {
	if opts.DryRun {
		expires := time.Now().UTC().Add(confirmTTL)
		token, err := generateConfirmToken(action, detail, expires)
		if err != nil {
			failErr("confirm token", err)
		}
		printJSON(map[string]any{
			"preview": map[string]any{
				"action":  action,
				"changes": []map[string]any{{"action": action, "detail": detail}},
			},
			"confirm_token": token,
			"expires_at":    expires.Format(time.RFC3339),
		})
		return true
	}
	if opts.Confirm == "" {
		fail(exitConfirmNeeded, "E_CONFIRMATION_REQUIRED", action+" requires --dry-run followed by --confirm <confirm_token>", nil, false)
	}
	if err := validateConfirmToken(action, detail, opts.Confirm, time.Now().UTC()); err != nil {
		fail(exitConflict, "E_CONFLICT", err.Error(), nil, false)
	}
	if err := consumeConfirmToken(opts.Confirm); err != nil {
		if !activeOutput.Quiet {
			fmt.Fprintf(os.Stderr, "warning: could not record consumed confirm token: %v\n", err)
		}
	}
	return false
}

func generateConfirmToken(action string, detail map[string]any, expires time.Time) (string, error) {
	payload := confirmPayload{Action: action, Detail: detail, ExpiresAt: expires.Unix()}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	secret, err := confirmSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payloadJSON)
	return "ct_" + base64.RawURLEncoding.EncodeToString(payloadJSON) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func validateConfirmToken(action string, detail map[string]any, token string, now time.Time) error {
	if isConfirmTokenConsumed(token) {
		return errors.New("confirm token already used; re-run --dry-run")
	}
	payload, err := parseConfirmToken(token)
	if err != nil {
		return err
	}
	if payload.Action != action {
		return fmt.Errorf("confirm token action mismatch: got %q, want %q", payload.Action, action)
	}
	if now.Unix() > payload.ExpiresAt {
		return errors.New("confirm token expired; re-run --dry-run")
	}
	wantDetail, _ := json.Marshal(canonical(detail))
	gotDetail, _ := json.Marshal(canonical(payload.Detail))
	if !hmac.Equal(wantDetail, gotDetail) {
		return errors.New("confirm token arguments changed; re-run --dry-run")
	}
	return nil
}

func parseConfirmToken(token string) (confirmPayload, error) {
	if !strings.HasPrefix(token, "ct_") {
		return confirmPayload{}, errors.New("invalid confirm token")
	}
	parts := strings.Split(strings.TrimPrefix(token, "ct_"), ".")
	if len(parts) != 2 {
		return confirmPayload{}, errors.New("invalid confirm token")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return confirmPayload{}, errors.New("invalid confirm token payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return confirmPayload{}, errors.New("invalid confirm token signature")
	}
	secret, err := confirmSecret()
	if err != nil {
		return confirmPayload{}, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payloadJSON)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return confirmPayload{}, errors.New("invalid confirm token signature")
	}
	var payload confirmPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return confirmPayload{}, err
	}
	return payload, nil
}

func canonical(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

func confirmSecret() ([]byte, error) {
	path := confirmSecretPath()
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		secret, decErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decErr == nil && len(secret) >= 32 {
			return secret, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}

func confirmSecretPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "confirm.secret")
}

func confirmConsumedPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "confirm-consumed.json")
}

func consumeConfirmToken(token string) error {
	path := confirmConsumedPath()
	consumed := readConsumedTokens(path)
	consumed[tokenFingerprint(token)] = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(consumed, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func isConfirmTokenConsumed(token string) bool {
	consumed := readConsumedTokens(confirmConsumedPath())
	_, ok := consumed[tokenFingerprint(token)]
	return ok
}

func readConsumedTokens(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	var consumed map[string]string
	if err := json.Unmarshal(data, &consumed); err != nil {
		return map[string]string{}
	}
	if consumed == nil {
		return map[string]string{}
	}
	return consumed
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
