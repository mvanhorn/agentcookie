package destination

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// ownedProfileIdentity is called only after Managed has verified its owned
// Chrome. A private durable token distinguishes a replacement profile at the
// same path from the original persistent profile. The path also prevents a
// copied marker in a different profile from inheriting ownership.
func ownedProfileIdentity(dir string) (string, error) {
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(canonical, ".agentcookie-profile-id")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 32 {
			return "", fmt.Errorf("invalid owned-profile identity file")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if raw, err := hex.DecodeString(string(b)); err != nil || len(raw) != 16 {
			return "", fmt.Errorf("invalid owned-profile identity")
		}
		return digest(append([]byte(canonical+"\x00"), b...)), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return "", err
	}
	b := []byte(hex.EncodeToString(token[:]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return ownedProfileIdentity(dir)
	}
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = syncDir(canonical); err != nil {
		return "", err
	}
	return digest(append([]byte(canonical+"\x00"), b...)), nil
}
