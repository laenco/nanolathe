package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/save"
)

func writeSlotTestPair(t *testing.T, path, bank, sidecar string) {
	t.Helper()
	for _, file := range [2]struct{ path, body string }{{path, bank}, {save.SidecarPath(path), sidecar}} {
		if err := os.WriteFile(file.path, []byte(file.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func checkSlotTestFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("file %s = %q, %v; want %q", path, got, err, want)
	}
}

// A failed capture and either publication failure must leave the previous
// bank and its selection byte-for-byte intact. In particular the second
// rename fails after the new bank has already replaced the old one.
func TestAutomaticSlotKeepsPreviousPairOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		captureFail bool
		failRename  int
	}{
		{name: "capture", captureFail: true},
		{name: "bank publication", failRename: 1},
		{name: "sidecar publication", failRename: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Autosave.SAV")
			writeSlotTestPair(t, path, "previous bank", "previous selection")
			failure := errors.New(tc.name + " failed")
			calls := 0
			err := replaceSlotSave(path, func(staged string) error {
				writeSlotTestPair(t, staged, "next bank", "next selection")
				if tc.captureFail {
					return failure
				}
				return nil
			}, func(from, to string) error {
				calls++
				if calls == tc.failRename {
					return failure
				}
				return os.Rename(from, to)
			})
			if !errors.Is(err, failure) {
				t.Fatalf("failure = %v, want %v", err, failure)
			}
			checkSlotTestFile(t, path, "previous bank")
			checkSlotTestFile(t, save.SidecarPath(path), "previous selection")
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("temporary files left after successful recovery: %v, %v", entries, err)
			}
		})
	}
}

func TestAutomaticSlotRequiresBothStagedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Quicksave.SAV")
	writeSlotTestPair(t, path, "previous bank", "previous selection")
	err := replaceSlotSave(path, func(staged string) error {
		return os.WriteFile(staged, []byte("next bank"), 0o644)
	}, func(string, string) error {
		t.Fatal("published before the sidecar was staged")
		return nil
	})
	if err == nil {
		t.Fatal("accepted a staged bank with no sidecar")
	}
	checkSlotTestFile(t, path, "previous bank")
	checkSlotTestFile(t, save.SidecarPath(path), "previous selection")
}

func TestAutomaticSlotRetainsRecoveryFilesWhenRollbackFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Autosave.SAV")
	writeSlotTestPair(t, path, "previous bank", "previous selection")
	calls := 0
	err := replaceSlotSave(path, func(staged string) error {
		writeSlotTestPair(t, staged, "next bank", "next selection")
		return nil
	}, func(from, to string) error {
		calls++
		if calls > 1 {
			return errors.New("publication and rollback blocked")
		}
		return os.Rename(from, to)
	})
	if err == nil {
		t.Fatal("lost the publication failure")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var recovery string
	for _, entry := range entries {
		if entry.IsDir() {
			recovery = filepath.Join(dir, entry.Name())
		}
	}
	if recovery == "" || !strings.Contains(err.Error(), recovery) {
		t.Fatalf("failure does not name its recovery directory %q: %v", recovery, err)
	}
	checkSlotTestFile(t, filepath.Join(recovery, "previous-bank"), "previous bank")
	checkSlotTestFile(t, filepath.Join(recovery, "previous-sidecar"), "previous selection")
	checkSlotTestFile(t, save.SidecarPath(path), "previous selection")
}

func TestAutomaticSlotRemovesNewBankWhenFirstPublicationHasNoPreviousSlot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Autosave.SAV")
	err := replaceSlotSave(path, func(staged string) error {
		writeSlotTestPair(t, staged, "next bank", "next selection")
		return nil
	}, func(from, to string) error {
		if to == save.SidecarPath(path) {
			return errors.New("sidecar publication blocked")
		}
		return os.Rename(from, to)
	})
	if err == nil {
		t.Fatal("lost the sidecar publication failure")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed first save left an incomplete slot: %v, %v", entries, readErr)
	}
}
