package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// unsubscribeToken is the token in user's unsubscribe links, which
// lierc-api checks (its accounts.CheckUnsubscribeToken): base64url
// (unpadded) of "<user>:<mac>", the mac the hex HMAC-SHA256 of
// "unsubscribe|<user>" keyed by UNSUBSCRIBE_SECRET. It doesn't expire.
func unsubscribeToken(secret, user string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("unsubscribe|" + user))
	return base64.RawURLEncoding.EncodeToString([]byte(user + ":" + hex.EncodeToString(m.Sum(nil))))
}
