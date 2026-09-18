// Package wordlist discovers and streams wordlist files stored in a managed
// directory.
package wordlist

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrAlreadyExists means the source basename is already a wordlist.
	ErrAlreadyExists = errors.New("wordlist already exists")
	// ErrInvalidSource means the source cannot be added as a wordlist.
	ErrInvalidSource = errors.New("invalid wordlist source")
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

// Add moves a regular file into the managed directory under its basename.
func (m *Manager) Add(source string) error {
	if !filepath.IsAbs(source) {
		return ErrInvalidSource
	}

	source = filepath.Clean(source)
	name := filepath.Base(source)
	if !ValidName(name) || pathWithin(m.wordlistDir, source) {
		return ErrInvalidSource
	}

	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("getting file info %s : %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidSource
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolving source wordlist %s : %w", source, err)
	}
	resolvedWordlistDir, err := filepath.EvalSymlinks(m.wordlistDir)
	if err != nil {
		return fmt.Errorf("resolving wordlist dir %s : %w", m.wordlistDir, err)
	}
	if pathWithin(resolvedWordlistDir, resolvedSource) {
		return ErrInvalidSource
	}

	destination := filepath.Join(m.wordlistDir, name)
	err = os.Link(source, destination)
	if err == nil {
		destinationInfo, statErr := os.Lstat(destination)
		if statErr != nil || !destinationInfo.Mode().IsRegular() || !os.SameFile(info, destinationInfo) {
			return errors.Join(ErrInvalidSource, statErr, os.Remove(destination))
		}
		currentSourceInfo, statErr := os.Lstat(source)
		if statErr != nil || !currentSourceInfo.Mode().IsRegular() || !os.SameFile(destinationInfo, currentSourceInfo) {
			return errors.Join(ErrInvalidSource, statErr, os.Remove(destination))
		}
		if err = os.Remove(source); err != nil {
			return errors.Join(fmt.Errorf("removing source wordlist %s : %w", source, err), os.Remove(destination))
		}
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return ErrAlreadyExists
	}

	return copyWordlist(source, destination, info)
}

func copyWordlist(source, destination string, sourceInfo os.FileInfo) (err error) {
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening source wordlist %s : %w", source, err)
	}
	defer sourceFile.Close()
	openedInfo, err := sourceFile.Stat()
	if err != nil {
		return fmt.Errorf("getting source wordlist info %s : %w", source, err)
	}
	currentSourceInfo, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("getting source wordlist info %s : %w", source, err)
	}
	if !currentSourceInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) || !os.SameFile(openedInfo, currentSourceInfo) {
		return ErrInvalidSource
	}

	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sourceInfo.Mode().Perm())
	if errors.Is(err, os.ErrExist) {
		return ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("creating wordlist %s : %w", destination, err)
	}

	removeDestination := true
	destinationClosed := false
	defer func() {
		if !destinationClosed {
			closeErr := destinationFile.Close()
			if err == nil && closeErr != nil {
				err = fmt.Errorf("closing wordlist %s : %w", destination, closeErr)
			}
		}
		if removeDestination {
			err = errors.Join(err, os.Remove(destination))
		}
	}()

	if _, err = io.Copy(destinationFile, sourceFile); err != nil {
		return fmt.Errorf("copying wordlist %s : %w", destination, err)
	}
	if err = destinationFile.Sync(); err != nil {
		return fmt.Errorf("syncing wordlist %s : %w", destination, err)
	}
	if err = destinationFile.Close(); err != nil {
		return fmt.Errorf("closing wordlist %s : %w", destination, err)
	}
	destinationClosed = true
	currentSourceInfo, err = os.Lstat(source)
	if err != nil {
		return fmt.Errorf("getting source wordlist info %s : %w", source, err)
	}
	if !currentSourceInfo.Mode().IsRegular() || !os.SameFile(openedInfo, currentSourceInfo) {
		return ErrInvalidSource
	}
	if err = os.Remove(source); err != nil {
		return fmt.Errorf("removing source wordlist %s : %w", source, err)
	}
	removeDestination = false
	return nil
}

func pathWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && (relative == "." || !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// ValidName reports whether name is one local filename.
func ValidName(name string) bool {
	return name != "" && !strings.ContainsRune(name, 0) && filepath.IsLocal(name) && filepath.Base(name) == name
}

// Open opens a regular wordlist file by name and returns an iterator for it.
func (m *Manager) Open(name string) (Iterator, error) {
	if !ValidName(name) {
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
