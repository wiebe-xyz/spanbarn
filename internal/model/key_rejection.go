package model

// KeyRejection counts the requests one unknown or revoked API key made. A pod
// that cannot write reports it to the writer, which adds it to key_rejections
// (#572). Fingerprint is the first eight hex digits of the key's SHA-256 hash:
// enough to match a key a sender still uses, and of no use for guessing it.
type KeyRejection struct {
	Fingerprint string `json:"f"`
	Count       int64  `json:"n"`
	LastSeen    int64  `json:"t"` // unix seconds of the newest rejected request
}
