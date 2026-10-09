package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
