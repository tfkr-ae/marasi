package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/extensions"
)

func init() {
	extensionCmd.AddCommand(extensionAPICmd)
}

var extensionAPICmd = &cobra.Command{
	Use:   "api [NAME]",
	Short: "Show the Lua API available to extensions",
	Long: `Show the Lua API available to extensions in this version of marasi.

Without NAME, list the hooks, globals, libraries, types, and standard Lua
functions. NAME is "hooks", a hook, a library such as marasi.crypto.aes, a
function such as marasi.repo:set_metadata, a type such as req, or a method
such as req:method. A function or method name may use . or :.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := extensions.Reference()
		stdout := cmd.OutOrStdout()
		if len(args) == 0 {
			if jsonOutput {
				return json.NewEncoder(stdout).Encode(ref)
			}
			return writeAPIOverview(stdout, ref)
		}
		item, ok := findAPIItem(ref, args[0])
		if !ok {
			return fmt.Errorf("no Lua API entry named %q; run marasi extension api to list names", args[0])
		}
		if jsonOutput {
			return json.NewEncoder(stdout).Encode(item)
		}
		return writeAPIItem(stdout, item)
	},
}

// Kinds of apiItem. They are the kind values in --json output.
const (
	apiKindHooks    = "hooks"
	apiKindHook     = "hook"
	apiKindGlobal   = "global"
	apiKindLibrary  = "library"
	apiKindFunction = "function"
	apiKindType     = "type"
	apiKindMethod   = "method"
)

// apiItem is one named part of the reference.
type apiItem struct {
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	Signature string             `json:"signature,omitempty"`
	Summary   string             `json:"summary,omitempty"`
	Entries   []extensions.Entry `json:"entries,omitempty"`
	// Libraries names the sub-libraries of a library.
	Libraries []string `json:"libraries,omitempty"`
}

func findAPIItem(ref extensions.APIReference, name string) (apiItem, bool) {
	if name == "hooks" {
		return apiItem{Kind: apiKindHooks, Name: name, Entries: ref.Hooks}, true
	}
	for _, hook := range ref.Hooks {
		if hook.Name == name {
			return apiItem{Kind: apiKindHook, Name: hook.Name, Signature: hook.Signature, Summary: hook.Summary}, true
		}
	}
	for _, global := range ref.Globals {
		if global.Name == name {
			return apiItem{Kind: apiKindGlobal, Name: global.Name, Signature: global.Signature, Summary: global.Summary}, true
		}
	}
	for _, library := range ref.Libraries {
		if library.Name == name {
			return apiItem{Kind: apiKindLibrary, Name: library.Name, Entries: library.Functions, Libraries: subLibraries(ref, library.Name)}, true
		}
	}
	if libraries := subLibraries(ref, name); len(libraries) > 0 {
		return apiItem{Kind: apiKindLibrary, Name: name, Libraries: libraries}, true
	}
	for _, typ := range ref.Types {
		if typ.Name == name {
			return apiItem{Kind: apiKindType, Name: typ.Name, Summary: typ.Summary, Entries: typ.Methods}, true
		}
	}
	// A function or method name splits at its last . or :.
	cut := strings.LastIndexAny(name, ".:")
	if cut <= 0 {
		return apiItem{}, false
	}
	owner, member := name[:cut], name[cut+1:]
	for _, library := range ref.Libraries {
		if library.Name == owner {
			if entry, ok := findEntry(library.Functions, member); ok {
				return apiItem{Kind: apiKindFunction, Name: owner + ":" + entry.Name, Signature: entry.Signature, Summary: entry.Summary}, true
			}
		}
	}
	for _, typ := range ref.Types {
		if typ.Name == owner {
			if entry, ok := findEntry(typ.Methods, member); ok {
				return apiItem{Kind: apiKindMethod, Name: owner + ":" + entry.Name, Signature: entry.Signature, Summary: entry.Summary}, true
			}
		}
	}
	return apiItem{}, false
}

func findEntry(entries []extensions.Entry, name string) (extensions.Entry, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return extensions.Entry{}, false
}

// subLibraries returns the libraries one level below name, including tables
// such as marasi.encoding that hold only libraries.
func subLibraries(ref extensions.APIReference, name string) []string {
	var names []string
	seen := map[string]bool{}
	for _, library := range ref.Libraries {
		rest, ok := strings.CutPrefix(library.Name, name+".")
		if !ok {
			continue
		}
		child, _, _ := strings.Cut(rest, ".")
		if !seen[child] {
			seen[child] = true
			names = append(names, name+"."+child)
		}
	}
	return names
}

func writeAPIOverview(w io.Writer, ref extensions.APIReference) error {
	var b strings.Builder
	b.WriteString(`Call library functions and object methods with a colon: marasi.strings:upper(s), request:method().
There is no string library, so use marasi.strings. A Lua error in a hook is
logged and the request, response, or message continues. A hook interrupted by
a project switch, service stop, or client disconnect drops it.

HOOKS
`)
	for _, hook := range ref.Hooks {
		fmt.Fprintf(&b, "  %s%s\n", hook.Name, hook.Signature)
	}
	b.WriteString("\nGLOBALS\n")
	for _, global := range ref.Globals {
		fmt.Fprintf(&b, "  %s%s\n", global.Name, global.Signature)
	}
	b.WriteString("\nLIBRARIES\n")
	if err := writeAPINameTable(&b, ref.Libraries); err != nil {
		return err
	}
	b.WriteString("\nTYPES\n")
	for _, typ := range ref.Types {
		fmt.Fprintf(&b, "  %s\n", typ.Name)
	}
	b.WriteString("\nSTANDARD LUA\n")
	if err := writeAPINameTable(&b, ref.Standard); err != nil {
		return err
	}
	b.WriteString("\nRun marasi extension api NAME for one hook, library, function, type, or method.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// writeAPINameTable writes each library's name and its function names, aligned.
func writeAPINameTable(w io.Writer, libraries []extensions.Library) error {
	table := newTableWriter(w)
	for _, library := range libraries {
		fmt.Fprintf(table, "  %s\t%s\n", library.Name, entryNames(library.Functions))
	}
	return table.Flush()
}

func writeAPIItem(w io.Writer, item apiItem) error {
	var b strings.Builder
	switch item.Kind {
	case apiKindHooks:
		for _, hook := range item.Entries {
			writeAPIEntry(&b, hook.Name, hook)
		}
	case apiKindLibrary:
		for _, function := range item.Entries {
			writeAPIEntry(&b, item.Name+":"+function.Name, function)
		}
		if len(item.Libraries) > 0 {
			b.WriteString("Libraries:\n")
			for _, library := range item.Libraries {
				fmt.Fprintf(&b, "  %s\n", library)
			}
		}
	case apiKindType:
		fmt.Fprintf(&b, "%s\n    %s\n\n", item.Name, item.Summary)
		for _, method := range item.Entries {
			writeAPIEntry(&b, item.Name+":"+method.Name, method)
		}
	default:
		writeAPIEntry(&b, item.Name, extensions.Entry{Signature: item.Signature, Summary: item.Summary})
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeAPIEntry(b *strings.Builder, name string, entry extensions.Entry) {
	fmt.Fprintf(b, "%s%s\n    %s\n", name, entry.Signature, entry.Summary)
}

func entryNames(entries []extensions.Entry) string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return strings.Join(names, " ")
}
