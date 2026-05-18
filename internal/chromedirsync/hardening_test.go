package chromedirsync

import (
	"archive/tar"
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// makeTar builds an in-memory tarball from the given members. Used by
// the hardening tests to construct hostile-shaped tarballs without
// touching disk.
func makeTar(t *testing.T, members []tar.Header, bodies map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, hdr := range members {
		h := hdr
		body := bodies[h.Name]
		h.Size = int64(len(body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("tar header %q: %v", h.Name, err)
		}
		if len(body) > 0 {
			if _, err := tw.Write(body); err != nil {
				t.Fatalf("tar body %q: %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// TestUnpack_RejectsPathTraversal exercises the canonical archive
// vulnerability: a member whose path resolves outside the staging dir.
func TestUnpack_RejectsPathTraversal(t *testing.T) {
	dst := t.TempDir()
	payload := makeTar(t, []tar.Header{
		{Name: "../escape.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string][]byte{"../escape.txt": []byte("hostile")})

	err := Unpack(payload, dst)
	if err == nil {
		t.Fatal("expected traversal rejection, got nil")
	}
	if !strings.Contains(err.Error(), "escapes staging dir") {
		t.Errorf("expected escape error, got %v", err)
	}
}

// TestUnpack_RejectsAbsolutePath: a tar entry with an absolute path
// (e.g. /etc/passwd) is refused.
func TestUnpack_RejectsAbsolutePath(t *testing.T) {
	dst := t.TempDir()
	payload := makeTar(t, []tar.Header{
		{Name: "/tmp/hostile", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string][]byte{"/tmp/hostile": []byte("nope")})

	err := Unpack(payload, dst)
	if err == nil {
		t.Fatal("expected absolute-path rejection, got nil")
	}
	if !strings.Contains(err.Error(), "absolute tar path") {
		t.Errorf("expected absolute-path error, got %v", err)
	}
}

// TestUnpack_RejectsSymlinkMember
func TestUnpack_RejectsSymlinkMember(t *testing.T) {
	dst := t.TempDir()
	payload := makeTar(t, []tar.Header{
		{Name: "evil-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777},
	}, nil)

	err := Unpack(payload, dst)
	if err == nil {
		t.Fatal("expected symlink rejection, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got %v", err)
	}
}

// TestUnpack_RejectsHardlinkMember
func TestUnpack_RejectsHardlinkMember(t *testing.T) {
	dst := t.TempDir()
	payload := makeTar(t, []tar.Header{
		{Name: "evil-hardlink", Typeflag: tar.TypeLink, Linkname: "anything", Mode: 0o644},
	}, nil)

	err := Unpack(payload, dst)
	if err == nil {
		t.Fatal("expected hardlink rejection, got nil")
	}
}

// TestUnpack_RejectsOversizedPayload
func TestUnpack_RejectsOversizedPayload(t *testing.T) {
	dst := t.TempDir()
	// A buffer of MaxUnpackBytes+1 bytes is enough to fail the size
	// check before any tar parsing.
	big := make([]byte, MaxUnpackBytes+1)
	err := Unpack(big, dst)
	if err == nil {
		t.Fatal("expected size-cap rejection, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("expected size-limit error, got %v", err)
	}
}

// TestUnpack_AcceptsNormalTarball is the regression: legitimate
// LocalStorage tarballs continue to unpack correctly.
func TestUnpack_AcceptsNormalTarball(t *testing.T) {
	dst := t.TempDir()
	payload := makeTar(t, []tar.Header{
		{Name: "subdir", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "subdir/file.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string][]byte{"subdir/file.txt": []byte("hello")})

	if err := Unpack(payload, dst); err != nil {
		t.Fatalf("normal tarball should unpack cleanly, got %v", err)
	}
	expected := filepath.Join(dst, "subdir", "file.txt")
	data, err := readFile(expected)
	if err != nil {
		t.Fatalf("expected file %s, got %v", expected, err)
	}
	if string(data) != "hello" {
		t.Errorf("file content mismatch: %q", string(data))
	}
}

func readFile(path string) ([]byte, error) {
	var b bytes.Buffer
	f, err := openRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := b.ReadFrom(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
