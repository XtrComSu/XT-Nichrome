package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureRecordsPrivateScreenAndContext(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		"grim":     "#!/bin/sh\n[ \"$1\" = '-c' ] || exit 2\nprintf PNG > \"$2\"\n",
		"hyprctl":  "#!/bin/sh\ncase \"$1\" in cursorpos) echo '321, 456';; activewindow) echo '{\"class\":\"Code\",\"title\":\"editor\"}';; esac\n",
		"wl-paste": "#!/bin/sh\nprintf selected\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	root := filepath.Join(t.TempDir(), "captures")
	snap, err := capture(root)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CursorX != 321 || snap.CursorY != 456 || snap.Selection != "selected" || snap.WindowClass != "Code" || snap.WindowTitle != "editor" {
		t.Fatalf("unexpected context: %+v", snap)
	}
	if !strings.HasPrefix(snap.Image, root+string(os.PathSeparator)) {
		t.Fatalf("image outside capture directory: %s", snap.Image)
	}
	for _, path := range []string{root, snap.Image, filepath.Join(root, "latest.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("%s has loose permissions: %v", path, info.Mode().Perm())
		}
	}
	latest, err := readLatest(root)
	if err != nil || latest.Image != snap.Image {
		t.Fatalf("latest mismatch: %+v, %v", latest, err)
	}
	newer, err := capture(root)
	if err != nil {
		t.Fatal(err)
	}
	if newer.Image == snap.Image {
		t.Fatal("second capture reused image path")
	}
	if _, err := os.Stat(snap.Image); !os.IsNotExist(err) {
		t.Fatalf("previous screenshot still retained: %v", err)
	}
}
