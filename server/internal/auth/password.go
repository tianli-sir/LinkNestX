package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

func HashPassword(password string) (string, error) {
	if password == "" || len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("password is required")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) bool {
	if len(password) > MaxPasswordBytes || len(encoded) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil || memory == 0 || timeCost == 0 || threads == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	expected, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(expected) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(got, expected) == 1
}

// Argon2idHash and Argon2idVerify are explicit aliases useful to callers.
func Argon2idHash(password string) (string, error) { return HashPassword(password) }
func Argon2idVerify(password, encoded string) bool { return VerifyPassword(password, encoded) }
