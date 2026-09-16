package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// EC-006: write's pre-read for the diff must not swallow errors other than
// NotExist. An unreadable existing file (EACCES/EIO) must abort the write
// without modifying the file and without an empty-diff success claim.
// The failure is injected via readHookForTest because running as root reads
// through chmod-based unreadability (uid 0 bypasses permission checks).
func TestWriteUnreadableAbortsWithoutModifyingFile(t *testing.T) {
	cases := []struct {
		name     string
		inject   error
		sentinel error
	}{
		{
			name:     "EACCES",
			inject:   &os.PathError{Op: "open", Path: "secret.txt", Err: syscall.EACCES},
			sentinel: syscall.EACCES,
		},
		{
			name:     "EIO",
			inject:   &os.PathError{Op: "read", Path: "secret.txt", Err: syscall.EIO},
			sentinel: syscall.EIO,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tempFS(t, map[string]string{"secret.txt": "original\n"})
			path := filepath.Join(f.Root, "secret.txt")
			f.readHookForTest = func(full string) error {
				return tc.inject
			}

			res, err := run(t, f.Tools(), "write", map[string]any{
				"path": "secret.txt", "content": "replacement\n",
			})
			if err == nil {
				t.Fatalf("write over unreadable file succeeded (diff=%q); want abort", res.Diff)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("write error = %v; want wrapped %v", err, tc.sentinel)
			}
			if !strings.Contains(err.Error(), "read existing file") {
				t.Errorf("error = %q; want it to mention the pre-read", err.Error())
			}

			// The file must be untouched: no truncate-and-replace happened.
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != "original\n" {
				t.Errorf("file = %q; want original contents preserved", got)
			}

			// No success claim and no empty-diff: the call failed, so there
			// is no diff pretending the file was empty before.
			if res.Diff != "" {
				t.Errorf("diff = %q on a failed write; want empty", res.Diff)
			}
			if res.DiffStat != nil {
				t.Errorf("DiffStat = %+v on a failed write; want nil", res.DiffStat)
			}
			if res.Output != "" {
				t.Errorf("output = %q on a failed write; want empty", res.Output)
			}
		})
	}

	t.Run("NoParentsOnFailure", func(t *testing.T) {
		f := tempFS(t, map[string]string{"keep.txt": "x\n"})
		f.readHookForTest = func(full string) error {
			return &os.PathError{Op: "open", Path: full, Err: syscall.EACCES}
		}
		_, err := run(t, f.Tools(), "write", map[string]any{
			"path": "newdir/nested/file.txt", "content": "hello\n",
		})
		if err == nil {
			t.Fatal("write with failing pre-read succeeded; want abort")
		}
		if !errors.Is(err, syscall.EACCES) {
			t.Fatalf("write error = %v; want wrapped EACCES", err)
		}
		if _, statErr := os.Stat(filepath.Join(f.Root, "newdir")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("newdir was created despite the aborted write (stat err = %v)", statErr)
		}
	})
}
