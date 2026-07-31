package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"evilcode/internal/provider"
)

// storedMessage is how a message is written to the log.
//
// Images do not go inline. Four attachments at the 4 MiB limit exceed the
// reader's 16 MiB record cap, and a record that cannot be parsed does not lose
// the images — it loses the session, since the read fails there and everything
// after it is unreachable. The bytes go beside the log and the record keeps a
// reference.
type storedMessage struct {
	provider.Message

	// Images shadows the embedded field so nothing is marshalled inline. It
	// keeps the [][]byte type on purpose: sessions written before this change
	// hold their attachments here, and decoding into anything else fails the
	// whole record — which does not lose the image, it loses the message.
	Images [][]byte `json:"images,omitempty"`
	Refs   []string `json:"image_refs,omitempty"`
}

// blobDir is where a session's attachments live.
func blobDir(path string) string {
	return strings.TrimSuffix(path, ".jsonl") + ".blobs"
}

// encodeMessage writes any attachments beside the log and returns the record.
func encodeMessage(path string, m provider.Message) ([]byte, error) {
	stored := storedMessage{Message: m}
	stored.Message.Images = nil

	if len(m.Images) > 0 {
		dir := blobDir(path)
		if err := os.MkdirAll(dir, DirPerm); err != nil {
			return nil, err
		}
		for _, img := range m.Images {
			sum := sha256.Sum256(img)
			name := hex.EncodeToString(sum[:]) + ".bin"
			// Content-addressed, so the same image attached twice is stored
			// once and a re-run writes nothing new.
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				if err := writeBlob(filepath.Join(dir, name), img); err != nil {
					return nil, err
				}
			}
			stored.Refs = append(stored.Refs, name)
		}
	}
	return json.Marshal(stored)
}

// writeBlob writes one attachment through a temp file, so a crash mid-write
// cannot leave a truncated image that a later resume would hand to a model.
func writeBlob(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blob.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(FilePerm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// decodeMessage parses a record and reattaches any referenced images.
//
// A missing blob is not fatal: the conversation is worth more than the
// attachment, and refusing to resume because an image was cleaned up would
// trade a small loss for a total one.
func decodeMessage(path string, data []byte) (provider.Message, error) {
	var stored storedMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		return provider.Message{}, err
	}
	m := stored.Message
	// Inline images are the old format, still in every session written before
	// the change.
	m.Images = stored.Images
	for _, ref := range stored.Refs {
		if ref == "" || filepath.Base(ref) != ref || strings.ContainsAny(ref, `/\\`) {
			continue
		}
		img, err := readBlob(filepath.Join(blobDir(path), ref))
		if err != nil {
			continue
		}
		m.Images = append(m.Images, img)
	}
	return m, nil
}

const maxBlobBytes = 4 << 20

func readBlob(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBlobBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBlobBytes {
		return nil, fmt.Errorf("image blob exceeds %d bytes", maxBlobBytes)
	}
	return data, nil
}

func readSessionFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
