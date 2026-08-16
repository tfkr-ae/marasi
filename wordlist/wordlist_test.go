package wordlist

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestNewManager(t *testing.T) {
	t.Run("should create wordlists directory", func(t *testing.T) {
		parentDir := t.TempDir()

		manager, err := NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := filepath.Join(parentDir, "wordlists")
		if manager.wordlistDir != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, manager.wordlistDir)
		}

		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if !info.IsDir() {
			t.Fatalf("\nwanted:\ndirectory\ngot:\n%v", info.Mode())
		}
	})

	t.Run("should raise an error if wordlists directory cannot be created", func(t *testing.T) {
		parentDir := filepath.Join(t.TempDir(), "file")
		err := os.WriteFile(parentDir, []byte("file"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = NewManager(parentDir)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestManagerList(t *testing.T) {
	t.Run("should list regular wordlists with their file sizes", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.WriteFile(filepath.Join(manager.wordlistDir, "passwords.txt"), []byte("admin\npassword\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		err = os.WriteFile(filepath.Join(manager.wordlistDir, "users.txt"), []byte("root\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := manager.List()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []Info{
			{Name: "passwords.txt", Size: 15},
			{Name: "users.txt", Size: 5},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should discover wordlists added after manager creation", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := manager.List()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if len(got) != 0 {
			t.Fatalf("\nwanted:\nempty list\ngot:\n%v", got)
		}

		err = os.WriteFile(filepath.Join(manager.wordlistDir, "users.txt"), []byte("admin\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err = manager.List()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []Info{{Name: "users.txt", Size: 6}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should skip directories and symbolic links", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links may require additional privileges on Windows")
		}

		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.Mkdir(filepath.Join(manager.wordlistDir, "directory"), 0700)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		target := filepath.Join(manager.wordlistDir, "target.txt")
		err = os.WriteFile(target, []byte("admin\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		err = os.Symlink(target, filepath.Join(manager.wordlistDir, "link.txt"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := manager.List()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []Info{{Name: "target.txt", Size: 6}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}

func TestManagerOpen(t *testing.T) {
	t.Run("should iterate over each wordlist line", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.WriteFile(filepath.Join(manager.wordlistDir, "words.txt"), []byte("one\n\ntwo\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		iterator, err := manager.Open("words.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer iterator.Close()

		var got []string
		for iterator.Scan() {
			got = append(got, iterator.Text())
		}
		if err = iterator.Err(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []string{"one", "", "two"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should reject invalid names", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		tests := []string{
			"",
			"../words.txt",
			filepath.Join("nested", "words.txt"),
			filepath.Join(manager.wordlistDir, "words.txt"),
		}

		for _, name := range tests {
			t.Run(strings.ReplaceAll(name, string(filepath.Separator), "_"), func(t *testing.T) {
				_, openErr := manager.Open(name)
				if openErr == nil {
					t.Fatal("\nwanted:\nerror\ngot:\nnil")
				}
			})
		}
	})

	t.Run("should reject directories", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.Mkdir(filepath.Join(manager.wordlistDir, "directory"), 0700)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = manager.Open("directory")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should reject symbolic links", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links may require additional privileges on Windows")
		}

		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		target := filepath.Join(manager.wordlistDir, "target.txt")
		err = os.WriteFile(target, []byte("admin\n"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		err = os.Symlink(target, filepath.Join(manager.wordlistDir, "link.txt"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = manager.Open("link.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})

	t.Run("should return scanner errors", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		content := strings.Repeat("a", bufio.MaxScanTokenSize+1)
		err = os.WriteFile(filepath.Join(manager.wordlistDir, "large.txt"), []byte(content), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		iterator, err := manager.Open("large.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer iterator.Close()

		got := iterator.Scan()
		if got {
			t.Fatalf("\nwanted:\nfalse\ngot:\n%v", got)
		}
		if iterator.Err() == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
	})
}

func TestIteratorClose(t *testing.T) {
	t.Run("should allow repeated calls", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "wordlist-*.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		iterator := newIterator(file)
		err = iterator.Close()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		err = iterator.Close()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})
}
