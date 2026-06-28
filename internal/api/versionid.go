package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// genVersionID produces a sortable, unique version ID (matches the S3 layer's
// format: nanosecond timestamp + random suffix).
func genVersionID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%016x%s", time.Now().UnixNano(), hex.EncodeToString(b))
}
