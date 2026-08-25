package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These target roughly 64 MiB and a few passes, which is
// comfortably above the OWASP floor and still fast enough for an interactive
// sign-in on a small instance.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16

	argonMinMemory  = 8 * 1024
	argonMaxMemory  = 256 * 1024
	argonMinTime    = 1
	argonMaxTime    = 10
	argonMinThreads = 1
	argonMaxThreads = 16
)

var ErrInvalidPasswordHash = errors.New("invalid password hash")

// HashPassword returns a PHC-formatted argon2id hash with a fresh random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches encoded. The comparison is
// constant time, and the parameters come from the stored hash so older hashes
// keep verifying after the cost settings change.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidPasswordHash
	}
	versionText, found := strings.CutPrefix(parts[2], "v=")
	version, err := strconv.Atoi(versionText)
	if !found || err != nil || version != argon2.Version || parts[2] != fmt.Sprintf("v=%d", version) {
		return false, ErrInvalidPasswordHash
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return false, ErrInvalidPasswordHash
	}
	memory, err := parseHashParameter(parameters[0], "m=", argonMinMemory, argonMaxMemory)
	if err != nil {
		return false, err
	}
	timeCost, err := parseHashParameter(parameters[1], "t=", argonMinTime, argonMaxTime)
	if err != nil {
		return false, err
	}
	threads, err := parseHashParameter(parameters[2], "p=", argonMinThreads, argonMaxThreads)
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltLen {
		return false, ErrInvalidPasswordHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != argonKeyLen {
		return false, ErrInvalidPasswordHash
	}
	got := argon2.IDKey([]byte(password), salt, uint32(timeCost), uint32(memory), uint8(threads), argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHashParameter(value, prefix string, minimum, maximum uint64) (uint64, error) {
	number, found := strings.CutPrefix(value, prefix)
	if !found || number == "" || strings.HasPrefix(number, "+") || (len(number) > 1 && number[0] == '0') {
		return 0, ErrInvalidPasswordHash
	}
	parsed, err := strconv.ParseUint(number, 10, 32)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, ErrInvalidPasswordHash
	}
	return parsed, nil
}

// dummyPasswordHash is verified against when no user matches, so a missing
// account costs the same time as a wrong password and cannot be distinguished.
var dummyPasswordHash, _ = HashPassword("contentflow-timing-equaliser")
