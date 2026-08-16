// Package wordlist discovers and streams wordlist files stored in a managed
// directory.
package wordlist

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Iterator reads a wordlist one entry at a time.
type Iterator interface {
	// Scan advances the iterator to the next wordlist entry.
	Scan() bool
	// Text returns the wordlist entry most recently read by Scan.
	Text() string
	// Err returns the first non-EOF error encountered by the iterator.
	Err() error
	// Close releases resources owned by the iterator.
	Close() error
}

// Provider lists and opens available wordlists.
type Provider interface {
	// List returns metadata for each available wordlist.
	List() ([]Info, error)
	// Open opens a wordlist by name.
	Open(name string) (Iterator, error)
}

// fileIterator reads entries from a wordlist file.
type fileIterator struct {
	// file is the open wordlist file owned by the iterator.
	file *os.File
	// scanner reads individual entries from file.
	scanner *bufio.Scanner
}

// Info describes an available wordlist.
type Info struct {
	// Name is the wordlist filename.
	Name string
	// Size is the wordlist file size in bytes.
	Size int64
}

// Manager discovers and opens wordlists in a configured directory.
type Manager struct {
	// wordlistDir is the directory containing managed wordlists.
	wordlistDir string
}

// NewManager creates a manager for the wordlists directory under parentDir.
func NewManager(parentDir string) (*Manager, error) {
	dir := filepath.Join(parentDir, "wordlists")

	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return nil, fmt.Errorf("creating wordlist dir %s : %w", dir, err)
	}

	return &Manager{
		wordlistDir: dir,
	}, nil
}

// List returns metadata for each regular wordlist file in the managed directory.
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.wordlistDir)
	if err != nil {
		return nil, fmt.Errorf("reading wordlist dir %s : %w", m.wordlistDir, err)
	}

	wordlists := make([]Info, 0, len(entries))

	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("getting file info %s : %w", entry.Name(), err)
		}

		if !info.Mode().IsRegular() {
			continue
		}

		wordlists = append(wordlists, Info{
			Name: entry.Name(),
			Size: info.Size(),
		})

	}

	return wordlists, nil
}

// Open opens a regular wordlist file by name and returns an iterator for it.
func (m *Manager) Open(name string) (Iterator, error) {
	if name == "" {
		return nil, errors.New("wordlist name cannot be empty")
	}

	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return nil, errors.New("wordlist name invalid")
	}

	wordlistPath := filepath.Join(m.wordlistDir, name)

	info, err := os.Lstat(wordlistPath)
	if err != nil {
		return nil, fmt.Errorf("getting file info %s : %w", wordlistPath, err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("wordlist %s is not a regular file", wordlistPath)
	}

	file, err := os.Open(wordlistPath)
	if err != nil {
		return nil, fmt.Errorf("reading wordlist %s : %w", wordlistPath, err)
	}

	return newIterator(file), nil
}

// newIterator creates a file iterator that owns file.
func newIterator(file *os.File) *fileIterator {
	return &fileIterator{
		file:    file,
		scanner: bufio.NewScanner(file),
	}
}

// Scan advances the iterator to the next wordlist entry.
func (i *fileIterator) Scan() bool {
	return i.scanner.Scan()
}

// Text returns the wordlist entry most recently read by Scan.
func (i *fileIterator) Text() string {
	return i.scanner.Text()
}

// Err returns the first non-EOF error encountered by the iterator.
func (i *fileIterator) Err() error {
	return i.scanner.Err()
}

// Close closes the wordlist file owned by the iterator.
func (i *fileIterator) Close() error {
	if i.file == nil {
		return nil
	}

	err := i.file.Close()
	i.file = nil

	return err
}
