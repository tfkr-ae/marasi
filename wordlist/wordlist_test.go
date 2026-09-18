package wordlist

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestManagerAdd(t *testing.T) {
	t.Run("should move a regular file into the wordlists directory", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		source := filepath.Join(t.TempDir(), "passwords.txt")
		if err = os.WriteFile(source, []byte("admin\npassword\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if err = manager.Add(source); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if _, err = os.Stat(source); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nsource removed\ngot:\n%v", err)
		}
		content, err := os.ReadFile(filepath.Join(manager.wordlistDir, "passwords.txt"))
		if err != nil || string(content) != "admin\npassword\n" {
			t.Fatalf("\nwanted:\nadded wordlist\ngot:\n%q, %v", content, err)
		}
	})

	t.Run("should leave the source in place when the name already exists", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		destination := filepath.Join(manager.wordlistDir, "passwords.txt")
		if err = os.WriteFile(destination, []byte("existing"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		source := filepath.Join(t.TempDir(), "passwords.txt")
		if err = os.WriteFile(source, []byte("new"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = manager.Add(source)
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrAlreadyExists, err)
		}
		if content, readErr := os.ReadFile(source); readErr != nil || string(content) != "new" {
			t.Fatalf("\nwanted:\nsource left in place\ngot:\n%q, %v", content, readErr)
		}
		if content, readErr := os.ReadFile(destination); readErr != nil || string(content) != "existing" {
			t.Fatalf("\nwanted:\nexisting wordlist unchanged\ngot:\n%q, %v", content, readErr)
		}
	})

	t.Run("should let only one manager claim a destination name", func(t *testing.T) {
		parentDir := t.TempDir()
		first, err := NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		second, err := NewManager(parentDir)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		firstSource := filepath.Join(t.TempDir(), "shared.txt")
		secondSource := filepath.Join(t.TempDir(), "shared.txt")
		if err = os.WriteFile(firstSource, []byte("first"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err = os.WriteFile(secondSource, []byte("second"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		start := make(chan struct{})
		results := make(chan error, 2)
		for index, add := range []func(string) error{first.Add, second.Add} {
			source := []string{firstSource, secondSource}[index]
			go func() {
				<-start
				results <- add(source)
			}()
		}
		close(start)
		firstErr, secondErr := <-results, <-results
		if !((firstErr == nil && errors.Is(secondErr, ErrAlreadyExists)) || (secondErr == nil && errors.Is(firstErr, ErrAlreadyExists))) {
			t.Fatalf("\nwanted:\none success and one %v\ngot:\n%v and %v", ErrAlreadyExists, firstErr, secondErr)
		}
	})

	t.Run("should reject invalid sources", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		outside := t.TempDir()
		regular := filepath.Join(outside, "regular.txt")
		if err = os.WriteFile(regular, []byte("word\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		inside := filepath.Join(manager.wordlistDir, "inside.txt")
		if err = os.WriteFile(inside, []byte("word\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		invalid := []string{
			"relative.txt",
			outside,
			inside,
			filepath.Join(outside, "bad\x00name"),
		}
		if runtime.GOOS != "windows" {
			symlink := filepath.Join(outside, "link.txt")
			if err = os.Symlink(regular, symlink); err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			if err = os.Symlink(manager.wordlistDir, alias); err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			invalid = append(invalid, symlink, filepath.Join(alias, "inside.txt"))
		}

		for _, source := range invalid {
			t.Run(strings.ReplaceAll(source, string(filepath.Separator), "_"), func(t *testing.T) {
				if addErr := manager.Add(source); !errors.Is(addErr, ErrInvalidSource) {
					t.Fatalf("\nwanted:\n%v\ngot:\n%v for %q", ErrInvalidSource, addErr, source)
				}
			})
		}

		missing := filepath.Join(outside, "missing.txt")
		if addErr := manager.Add(missing); !errors.Is(addErr, os.ErrNotExist) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", os.ErrNotExist, addErr)
		}
	})
}

func TestManagerRemove(t *testing.T) {
	t.Run("should remove a wordlist while an open iterator keeps reading", func(t *testing.T) {
		manager, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		path := filepath.Join(manager.wordlistDir, "passwords.txt")
		if err = os.WriteFile(path, []byte("admin\npassword\n"), 0600); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		iterator, err := manager.Open("passwords.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer iterator.Close()

		if err = manager.Remove("passwords.txt"); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("\nwanted:\nwordlist removed\ngot:\n%v", err)
		}
		if _, err = manager.Open("passwords.txt"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", os.ErrNotExist, err)
		}
		var entries []string
		for iterator.Scan() {
			entries = append(entries, iterator.Text())
		}
		if err = iterator.Err(); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []string{"admin", "password"}
		if !reflect.DeepEqual(entries, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, entries)
		}
	})
}

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
