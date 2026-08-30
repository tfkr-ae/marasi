package armory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tfkr-ae/marasi/wordlist"
)

type testAttackWordlistProvider struct {
	wordlist.Provider
	entries        []string
	entriesByName  map[string][]string
	openErr        error
	openErrByName  map[string]error
	readErr        error
	readErrByName  map[string]error
	closeErr       error
	closeErrByName map[string]error
	opened         []string
	created        []*testAttackWordlistIterator
}

type testAttackWordlistIterator struct {
	entries  []string
	index    int
	err      error
	closeErr error
	closed   bool
}

func (provider *testAttackWordlistProvider) Open(name string) (wordlist.Iterator, error) {
	provider.opened = append(provider.opened, name)
	if provider.openErr != nil {
		return nil, provider.openErr
	}
	if err := provider.openErrByName[name]; err != nil {
		return nil, err
	}

	entries := provider.entries
	if configured, exists := provider.entriesByName[name]; exists {
		entries = configured
	}
	readErr := provider.readErr
	if configured, exists := provider.readErrByName[name]; exists {
		readErr = configured
	}
	closeErr := provider.closeErr
	if configured, exists := provider.closeErrByName[name]; exists {
		closeErr = configured
	}

	iterator := &testAttackWordlistIterator{
		entries:  entries,
		index:    -1,
		err:      readErr,
		closeErr: closeErr,
	}
	provider.created = append(provider.created, iterator)
	return iterator, nil
}

func (iterator *testAttackWordlistIterator) Scan() bool {
	iterator.index++
	return iterator.index < len(iterator.entries)
}

func (iterator *testAttackWordlistIterator) Text() string {
	return iterator.entries[iterator.index]
}

func (iterator *testAttackWordlistIterator) Err() error {
	return iterator.err
}

func (iterator *testAttackWordlistIterator) Close() error {
	iterator.closed = true
	return iterator.closeErr
}

func TestManager_ProduceHarpoon(t *testing.T) {
	t.Run("should apply each wordlist entry to each template position in turn", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one", "", "two"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 6),
			ctx:      context.Background(),
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		close(execution.requests)
		got := make([]string, 0, 6)
		for request := range execution.requests {
			got = append(got, request)
		}

		want := []string{
			"username=one&password=secret",
			"username=&password=secret",
			"username=two&password=secret",
			"username=admin&password=one",
			"username=admin&password=",
			"username=admin&password=two",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

		wantOpened := []string{"words.txt", "words.txt"}
		if !reflect.DeepEqual(provider.opened, wantOpened) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOpened, provider.opened)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if the wordlist cannot be opened", func(t *testing.T) {
		provider := &testAttackWordlistProvider{openErr: errors.New("open failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "opening wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'opening wordlist words.txt'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the wordlist cannot be read", func(t *testing.T) {
		provider := &testAttackWordlistProvider{readErr: errors.New("read failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "reading wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'reading wordlist words.txt'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should return an error if the wordlist cannot be closed", func(t *testing.T) {
		provider := &testAttackWordlistProvider{closeErr: errors.New("close failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "closing wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'closing wordlist words.txt'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the template cannot be rendered", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate(`{{index "x" 2}}@@value@@`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "rendering armory template") {
			t.Fatalf("\nwanted:\nerror containing 'rendering armory template'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should stop when the execution is cancelled", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		execution := &execution{
			requests: make(chan string),
			ctx:      ctx,
		}

		err = manager.produceHarpoon(execution, tmpl, "words.txt")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})
}

func TestManager_ProduceBroadside(t *testing.T) {
	t.Run("should apply each wordlist entry to every template position", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one", "", "two"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 3),
			ctx:      context.Background(),
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		close(execution.requests)
		got := make([]string, 0, 3)
		for request := range execution.requests {
			got = append(got, request)
		}

		want := []string{
			"username=one&password=one",
			"username=&password=",
			"username=two&password=two",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

		wantOpened := []string{"words.txt"}
		if !reflect.DeepEqual(provider.opened, wantOpened) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOpened, provider.opened)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should return an error if the wordlist cannot be opened", func(t *testing.T) {
		provider := &testAttackWordlistProvider{openErr: errors.New("open failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "opening wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'opening wordlist words.txt'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the wordlist cannot be read", func(t *testing.T) {
		provider := &testAttackWordlistProvider{readErr: errors.New("read failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "reading wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'reading wordlist words.txt'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should return an error if the wordlist cannot be closed", func(t *testing.T) {
		provider := &testAttackWordlistProvider{closeErr: errors.New("close failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "closing wordlist words.txt") {
			t.Fatalf("\nwanted:\nerror containing 'closing wordlist words.txt'\ngot:\n%v", err)
		}
	})

	t.Run("should return an error if the template cannot be rendered", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate(`{{index "x" 2}}@@value@@`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "rendering armory template") {
			t.Fatalf("\nwanted:\nerror containing 'rendering armory template'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should stop when the execution is cancelled", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		execution := &execution{
			requests: make(chan string),
			ctx:      ctx,
		}

		err = manager.produceBroadside(execution, tmpl, "words.txt")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})
}

func TestManager_ProduceTandem(t *testing.T) {
	t.Run("should advance each position wordlist together", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt":     {"alice", "", "bob"},
				"passwords.txt": {"pass1", "pass2", "pass3"},
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 3),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		close(execution.requests)
		got := make([]string, 0, 3)
		for request := range execution.requests {
			got = append(got, request)
		}

		want := []string{
			"username=alice&password=pass1",
			"username=&password=pass2",
			"username=bob&password=pass3",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

		wantOpened := []string{"users.txt", "passwords.txt"}
		if !reflect.DeepEqual(provider.opened, wantOpened) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOpened, provider.opened)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should stop when the shortest wordlist ends", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt":     {"alice"},
				"passwords.txt": {"pass1", "pass2"},
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		close(execution.requests)
		got := make([]string, 0, 1)
		for request := range execution.requests {
			got = append(got, request)
		}
		want := []string{"username=alice&password=pass1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should return an error if no wordlists are provided", func(t *testing.T) {
		manager := &Manager{wordlists: &testAttackWordlistProvider{}}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, nil)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "tandem requires at least one wordlist") {
			t.Fatalf("\nwanted:\nerror containing 'tandem requires at least one wordlist'\ngot:\n%v", err)
		}
	})

	t.Run("should close opened wordlists if another wordlist cannot be opened", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			openErrByName: map[string]error{
				"passwords.txt": errors.New("open failed"),
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "opening wordlist passwords.txt") {
			t.Fatalf("\nwanted:\nerror containing 'opening wordlist passwords.txt'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should return an error if a wordlist cannot be read", func(t *testing.T) {
		provider := &testAttackWordlistProvider{readErr: errors.New("read failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "reading wordlist users.txt") {
			t.Fatalf("\nwanted:\nerror containing 'reading wordlist users.txt'\ngot:\n%v", err)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if a wordlist cannot be closed", func(t *testing.T) {
		provider := &testAttackWordlistProvider{closeErr: errors.New("close failed")}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "closing wordlist users.txt") {
			t.Fatalf("\nwanted:\nerror containing 'closing wordlist users.txt'\ngot:\n%v", err)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if the template cannot be rendered", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate(`{{index "x" 2}}@@value@@`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceTandem(execution, tmpl, []string{"words.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "rendering armory template") {
			t.Fatalf("\nwanted:\nerror containing 'rendering armory template'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should stop when the execution is cancelled", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		execution := &execution{
			requests: make(chan string),
			ctx:      ctx,
		}

		err = manager.produceTandem(execution, tmpl, []string{"words.txt"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})
}

func TestManager_ProduceMaelstrom(t *testing.T) {
	t.Run("should generate every combination of the position wordlists", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt":     {"alice", "bob"},
				"passwords.txt": {"pass1", "", "pass2"},
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 6),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		close(execution.requests)
		got := make([]string, 0, 6)
		for request := range execution.requests {
			got = append(got, request)
		}

		want := []string{
			"username=alice&password=pass1",
			"username=alice&password=",
			"username=alice&password=pass2",
			"username=bob&password=pass1",
			"username=bob&password=",
			"username=bob&password=pass2",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}

		wantOpened := []string{"users.txt", "passwords.txt", "passwords.txt"}
		if !reflect.DeepEqual(provider.opened, wantOpened) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantOpened, provider.opened)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if no wordlists are provided", func(t *testing.T) {
		manager := &Manager{wordlists: &testAttackWordlistProvider{}}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, nil)
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "maelstrom requires at least one wordlist") {
			t.Fatalf("\nwanted:\nerror containing 'maelstrom requires at least one wordlist'\ngot:\n%v", err)
		}
	})

	t.Run("should close outer wordlists if an inner wordlist cannot be opened", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt": {"alice"},
			},
			openErrByName: map[string]error{
				"passwords.txt": errors.New("open failed"),
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "opening wordlist passwords.txt") {
			t.Fatalf("\nwanted:\nerror containing 'opening wordlist passwords.txt'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should return an error if an inner wordlist cannot be read", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt": {"alice"},
			},
			readErrByName: map[string]error{
				"passwords.txt": errors.New("read failed"),
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "reading wordlist passwords.txt") {
			t.Fatalf("\nwanted:\nerror containing 'reading wordlist passwords.txt'\ngot:\n%v", err)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if an inner wordlist cannot be closed", func(t *testing.T) {
		provider := &testAttackWordlistProvider{
			entriesByName: map[string][]string{
				"users.txt": {"alice"},
			},
			closeErrByName: map[string]error{
				"passwords.txt": errors.New("close failed"),
			},
		}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("username=@@admin@@&password=@@secret@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"users.txt", "passwords.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "closing wordlist passwords.txt") {
			t.Fatalf("\nwanted:\nerror containing 'closing wordlist passwords.txt'\ngot:\n%v", err)
		}
		for _, iterator := range provider.created {
			if !iterator.closed {
				t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
			}
		}
	})

	t.Run("should return an error if the template cannot be rendered", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate(`{{index "x" 2}}@@value@@`)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		execution := &execution{
			requests: make(chan string, 1),
			ctx:      context.Background(),
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"words.txt"})
		if err == nil {
			t.Fatal("\nwanted:\nerror\ngot:\nnil")
		}
		if !strings.Contains(err.Error(), "rendering armory template") {
			t.Fatalf("\nwanted:\nerror containing 'rendering armory template'\ngot:\n%v", err)
		}
		if !provider.created[0].closed {
			t.Fatal("\nwanted:\nclosed iterator\ngot:\nopen iterator")
		}
	})

	t.Run("should stop when the execution is cancelled", func(t *testing.T) {
		provider := &testAttackWordlistProvider{entries: []string{"one"}}
		manager := &Manager{wordlists: provider}
		tmpl, err := parseTemplate("value=@@fallback@@")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		execution := &execution{
			requests: make(chan string),
			ctx:      ctx,
		}

		err = manager.produceMaelstrom(execution, tmpl, []string{"words.txt"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", context.Canceled, err)
		}
		if len(provider.created) != 0 {
			t.Fatalf("\nwanted:\n0 opened iterators\ngot:\n%d", len(provider.created))
		}
	})
}
