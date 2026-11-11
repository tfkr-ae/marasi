package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tfkr-ae/marasi/skills"
)

// verifySkillDir holds the verify-marasi skill, relative to the module root.
const verifySkillDir = ".agents/skills/verify-marasi"

// skillDriftAllowed lists mentions that may disagree with the CLI on purpose,
// keyed by file and the exact span or command text, with the reason.
var skillDriftAllowed = map[string]string{
	".agents/skills/verify-marasi/features/proxy-listener-control.md: listener restart": "the feature map says there is no such command",
}

// TestSkillProseMatchesCLI checks the marasi commands and flags that the
// printed skill and the verify-marasi skill mention in prose, so neither can
// drift from the CLI. TestSkillCommandsExist covers the printed skill's sh
// blocks; this test covers the rest.
//
// It reads these mentions:
//
//   - Commands in fenced sh and bash blocks of the verify-marasi files. A
//     command is a pipeline segment, `&&` or `;` part, or `$(…)` body whose
//     first word after any VAR=value prefixes is a marasi binary (`marasi`,
//     `./dist/marasi`).
//   - Inline code spans in both skills. A span that starts with a marasi binary
//     is a command. A span that starts with a top-level command name, such as
//     `service start --port 0`, is a command without the binary.
//
// For a command it walks the words: a word that names a subcommand extends the
// command path, the first other word ends the path, and every later word that
// is not a flag is an argument and is skipped. That covers placeholders such
// as `<id>`, `…` and `"$VAR"`, and redirections such as `</dev/null`. `[--ca <pem-file>]` counts as `--ca`.
// A word `start|update` in the path means either command, and `…|--no-ca` means the
// flag. Every flag must be a flag of the command or a persistent flag of one
// of its parents.
//
// A span without the binary counts as a command only when the command would
// take the words after its path as arguments, allowing for left-out ones such
// as the id in `traffic get --json`. That skips quoted error messages that
// start with a command name.
//
// A span can also name a subcommand of a command mentioned in the same table
// row, list item or paragraph, as in "`service control start` and
// `update`", or "`traffic count`, … `count --after <as_of>`". A span that
// starts with a word that is not a top-level command resolves against each
// command the block mentions and that command's parents. A span that resolves
// to no command is prose, such as a JSON field name, and is skipped. A span
// that resolves under a mentioned command is read only that way, so `update`
// next to `service control start` is not also `marasi update`; otherwise a
// span with several readings passes when one of them accepts all its flags.
//
// A span without the binary that names a command group and then one bare word
// that is none of its subcommands, such as `listener bogus`, is reported as an
// unknown subcommand.
//
// A span that is only a flag, such as `--address` or `--status in_progress`,
// is checked in table rows, where each row describes one command group: the
// flag must belong to a command the row mentions, to one of that command's
// subcommands, or to a parent as a persistent flag. Flag spans elsewhere are
// not checked, because a paragraph often names a flag next to commands that
// do not take it ("set it with `--password` on `service start`; read it with
// `service status`"). Rows that mention no command, such as option tables,
// are skipped too.
//
// Add a deliberate mismatch, such as a note about a removed flag, to
// skillDriftAllowed with its reason instead of loosening these rules.
func TestSkillProseMatchesCLI(t *testing.T) {
	docs := map[string]string{"skills/marasi/SKILL.md": skills.Marasi}
	root := moduleRoot(t)
	err := filepath.WalkDir(filepath.Join(root, verifySkillDir), func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(file) != ".md" {
			return err
		}
		text, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, file)
		docs[filepath.ToSlash(rel)] = string(text)
		return err
	})
	if err != nil {
		t.Fatalf("\nwanted:\nthe verify-marasi skill files\ngot:\n%v", err)
	}
	if len(docs) < 2 {
		t.Fatalf("\nwanted:\nverify-marasi files under %s\ngot:\nnone", verifySkillDir)
	}

	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}
	slices.Sort(names)

	used := map[string]bool{}
	for _, name := range names {
		t.Run("should mention only real commands and flags in "+name, func(t *testing.T) {
			problems, checked := skillDriftProblems(docs[name], strings.HasPrefix(name, verifySkillDir+"/"))
			if name == "skills/marasi/SKILL.md" && checked == 0 {
				t.Fatal("\nwanted:\nmarasi commands in the skill's prose\ngot:\nnone")
			}
			for _, problem := range problems {
				key := name + ": " + problem.text
				if _, ok := skillDriftAllowed[key]; ok {
					used[key] = true
					continue
				}
				t.Errorf("\nwanted:\n%s\ngot:\n%s:%d: %s in %q", problem.wanted, name, problem.line, problem.got, problem.text)
			}
		})
	}
	t.Run("should use every allowlist entry", func(t *testing.T) {
		keys := make([]string, 0, len(skillDriftAllowed))
		for key := range skillDriftAllowed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if !used[key] {
				t.Errorf("\nwanted:\na mismatch for allowlist entry\ngot:\nnone for %q", key)
			}
		}
	})
}

// TestSkillDriftParsing pins how mentions are read, so a parser change cannot
// quietly stop checking them.
func TestSkillDriftParsing(t *testing.T) {
	tests := []struct {
		name, doc string
		verify    bool
		want      []string
	}{
		{
			name: "should report an unknown flag in a table row",
			doc:  "| Task | Command group | Guidance |\n| --- | --- | --- |\n| Move | `listener` | `start` takes `--bogus-flag`. |\n",
			want: []string{"--bogus-flag"},
		},
		{
			name: "should accept a subcommand's flag in a table row",
			doc:  "| Move | `listener` | `start` requires `--address` and `--port`. |\n",
		},
		{
			name: "should report an unknown flag in a command span",
			doc:  "Run `marasi service start --bogus-flag 1`.\n",
			want: []string{"--bogus-flag"},
		},
		{
			name: "should report an unknown command in a marasi span",
			doc:  "Run `marasi service bogus --json`.\n",
			want: []string{"bogus"},
		},
		{
			name: "should report an unknown subcommand in a span without the binary",
			doc:  "Run `service bogus --json`.\n",
			want: []string{"bogus"},
		},
		{
			name: "should report an unknown subcommand in a table row",
			doc:  "| Move | `listener` | `listener bogus` lists. |\n",
			want: []string{"bogus"},
		},
		{
			name: "should skip a message that starts with a command group",
			doc:  "It prints `service is not running`.\n",
		},
		{
			name: "should report an unknown flag in a span without the binary",
			doc:  "- Start with `service start --bogus-flag HOST:PORT`.\n",
			want: []string{"--bogus-flag"},
		},
		{
			name: "should resolve a span against a command earlier in the block",
			doc:  "Use `service control start` and `update --address A --port P`.\n",
		},
		{
			name: "should resolve alternatives",
			doc:  "Change it with `service control start|update --address A`.\n",
		},
		{
			name: "should skip placeholders, quotes and brackets",
			doc:  "| Manage | `upstream` | `update <name> [--url …] [--ca …\\|--no-ca]`, `use --none`, `remove \"$NAME\"`. |\n",
		},
		{
			name: "should skip redirections",
			doc:  "Run `traffic bulk note append \"noted\" --bogus-flag 1 </dev/null 2>&1`.\n",
			want: []string{"--bogus-flag"},
		},
		{
			name:   "should check a verify-marasi bash block",
			verify: true,
			doc:    "```bash\nID=\"$(./dist/marasi --instance x traffic list --json | jq -r .)\" && \\\n  marasi service status --bogus-flag\n```\n",
			want:   []string{"--bogus-flag"},
		},
		{
			name: "should skip a flag span outside a table",
			doc:  "Set `--bogus-flag` on `service start`.\n",
		},
		{
			name: "should skip prose spans",
			doc:  "| Watch | `events` | `stream.reset` with `reason` `expired`. |\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problems, _ := skillDriftProblems(test.doc, test.verify)
			var got []string
			for _, problem := range problems {
				got = append(got, problem.flag)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q (%+v)", test.want, got, problems)
			}
		})
	}
}

// skillDriftProblem is a mention that does not match the CLI.
type skillDriftProblem struct {
	line        int
	text        string
	flag        string
	wanted, got string
}

// skillDriftProblems returns the mentions in doc that do not match rootCmd and
// the number of commands it checked. checkFences also checks commands in sh
// and bash blocks.
func skillDriftProblems(doc string, checkFences bool) (problems []skillDriftProblem, checked int) {
	for _, block := range markdownBlocks(doc) {
		if block.fence {
			if !checkFences {
				continue
			}
			for _, command := range fenceCommands(block) {
				checked++
				problems = append(problems, checkSpanCommand(command.text, command.line, command.tokens, rootCmd, true)...)
			}
			continue
		}
		// context holds the commands the block mentions; seeds also holds
		// the top-level readings of spans without the binary, which only
		// supply parents for other spans.
		var context, seeds []*cobra.Command
		var later []codeSpan
		for _, span := range codeSpans(block) {
			for _, tokens := range shellSegments(span.text) {
				tokens = skipAssignments(tokens)
				switch {
				case len(tokens) == 0:
				case isMarasiBinary(tokens[0]):
					checked++
					command, _, _, _ := walkCommand(rootCmd, tokens[1:])
					context = append(context, command)
					problems = append(problems, checkSpanCommand(span.text, span.line, tokens[1:], rootCmd, true)...)
				case strings.HasPrefix(tokens[0], "-"):
					later = append(later, codeSpan{text: span.text, line: span.line, tokens: tokens})
				case childCommand(rootCmd, firstAlternative(tokens[0])) != nil:
					for _, variant := range expandAlternatives(tokens) {
						command, _, _, _ := walkCommand(rootCmd, variant)
						seeds = append(seeds, command)
					}
					later = append(later, codeSpan{text: span.text, line: span.line, tokens: tokens})
				default:
					later = append(later, codeSpan{text: span.text, line: span.line, tokens: tokens})
				}
			}
		}

		// Resolve spans against the block's commands and their parents.
		var parents []*cobra.Command
		for _, command := range append(slices.Clone(context), seeds...) {
			for c := command; c != nil; c = c.Parent() {
				if !slices.Contains(parents, c) {
					parents = append(parents, c)
				}
			}
		}
		if !slices.Contains(parents, rootCmd) {
			parents = append(parents, rootCmd)
		}
		var flagSpans []codeSpan
		for _, span := range later {
			if strings.HasPrefix(span.tokens[0], "-") {
				flagSpans = append(flagSpans, span)
				continue
			}
			// A reading under a command the block mentions wins over the
			// top-level one, so `update` next to `service control start`
			// is not also `marasi update`.
			var resolved, nested []*cobra.Command
			var best []skillDriftProblem
			for _, parent := range parents {
				for _, variant := range expandAlternatives(span.tokens) {
					command, consumed, _, args := walkCommand(parent, variant)
					if consumed == 0 || refusesArgs(command, args) {
						// No command, or words the command would refuse,
						// such as an error message that starts with a
						// command name.
						continue
					}
					found := checkSpanCommand(span.text, span.line, variant, parent, false)
					if len(found) == 0 {
						resolved = append(resolved, command)
						if parent != rootCmd {
							nested = append(nested, command)
						}
					} else if best == nil {
						best = found
					}
				}
			}
			if len(nested) > 0 {
				resolved = nested
			}
			switch unknown := unknownSubcommand(span); {
			case len(resolved) > 0:
				checked++
				context = append(context, resolved...)
			case best != nil:
				checked++
				problems = append(problems, best...)
			case unknown != nil:
				checked++
				problems = append(problems, *unknown)
			}
		}

		if !block.tableRow || len(context) == 0 {
			continue
		}
		for _, span := range flagSpans {
			for _, flag := range spanFlags(span.tokens) {
				if !slices.ContainsFunc(context, func(command *cobra.Command) bool { return treeAcceptsFlag(command, flag) }) {
					paths := make([]string, 0, len(context))
					for _, command := range context {
						if path := command.CommandPath(); !slices.Contains(paths, path) {
							paths = append(paths, path)
						}
					}
					problems = append(problems, skillDriftProblem{
						line:   span.line,
						text:   span.text,
						flag:   flag,
						wanted: fmt.Sprintf("a flag of %s or its subcommands", strings.Join(paths, ", ")),
						got:    "unknown flag " + flag + " in the table row",
					})
				}
			}
		}
	}
	return problems, checked
}

// checkSpanCommand checks the command that tokens name, starting from parent.
// explicit reports a span whose words name no subcommand of parent.
func checkSpanCommand(text string, line int, tokens []string, parent *cobra.Command, explicit bool) []skillDriftProblem {
	var problems []skillDriftProblem
	for _, variant := range expandAlternatives(tokens) {
		command, _, flags, args := walkCommand(parent, variant)
		if explicit && command.HasSubCommands() && refusesArgs(command, args) {
			problems = append(problems, skillDriftProblem{
				line:   line,
				text:   text,
				flag:   args[0],
				wanted: "a subcommand of " + command.CommandPath(),
				got:    "unknown command " + args[0],
			})
		}
		for _, flag := range flags {
			if acceptsFlag(command, flag) {
				continue
			}
			problems = append(problems, skillDriftProblem{
				line:   line,
				text:   text,
				flag:   flag,
				wanted: fmt.Sprintf("%s to accept %s", command.CommandPath(), flag),
				got:    "unknown flag " + flag,
			})
		}
	}
	return problems
}

// walkCommand follows the subcommand words in tokens from parent. It returns
// the command, how many words named subcommands, every flag in tokens, and
// the arguments: the other words, without flag values and ellipses.
func walkCommand(parent *cobra.Command, tokens []string) (command *cobra.Command, consumed int, flags, args []string) {
	command = parent
	inPath := true
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if redirection := shellRedirection.FindString(token); redirection != "" {
			if redirection == token {
				i++ // The target is the next word.
			}
			continue
		}
		if flag, ok := tokenFlag(token); ok {
			flags = append(flags, flag)
			// An unknown flag is taken to have a value, as most flags do.
			f := lookupFlag(command, flag)
			if (f == nil || f.NoOptDefVal == "") && !strings.Contains(token, "=") && i+1 < len(tokens) {
				if _, next := tokenFlag(tokens[i+1]); !next {
					i++
				}
			}
			continue
		}
		if inPath {
			if child := childCommand(command, token); child != nil {
				command = child
				consumed++
				continue
			}
			inPath = false
		}
		if token != "…" && token != "..." {
			args = append(args, token)
		}
	}
	return command, consumed, flags, args
}

// bareWord matches a word that could name a subcommand, so a span such as
// `listener bogus` reads as a command while `service is not running` does not.
var bareWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// unknownSubcommand reports a span without the binary that names a top-level
// command group and then one word that is none of its subcommands, as a
// removed or renamed subcommand leaves behind. Only a single bare word counts,
// so a quoted message that starts with a command name stays prose.
func unknownSubcommand(span codeSpan) *skillDriftProblem {
	tokens := expandAlternatives(span.tokens)[0]
	if childCommand(rootCmd, tokens[0]) == nil {
		return nil
	}
	command, _, _, args := walkCommand(rootCmd, tokens)
	if !command.HasSubCommands() || command.Args != nil || len(args) != 1 || !bareWord.MatchString(args[0]) {
		return nil
	}
	return &skillDriftProblem{
		line:   span.line,
		text:   span.text,
		flag:   args[0],
		wanted: "a subcommand of " + command.CommandPath(),
		got:    "unknown command " + args[0],
	}
}

// omittedArgsLimit is how many arguments a span may leave out, as
// `traffic get --json` leaves out the id.
const omittedArgsLimit = 3

// refusesArgs reports whether command would refuse args even with up to
// omittedArgsLimit more. A command group without an Args rule refuses any
// argument, since it is then an unknown subcommand.
func refusesArgs(command *cobra.Command, args []string) bool {
	if len(args) > 0 && command.HasSubCommands() && command.Args == nil {
		return true
	}
	for omitted := 0; omitted <= omittedArgsLimit; omitted++ {
		if command.ValidateArgs(append(slices.Clone(args), make([]string, omitted)...)) == nil {
			return false
		}
	}
	return true
}

// shellRedirection matches a redirection such as </dev/null, 2>&1 or >, but
// not a placeholder such as <id>.
var shellRedirection = regexp.MustCompile(`^(\d*>>?&?|<)(?:[^>]*$)`)

// tokenFlag returns the flag a word names, as --name or -x.
func tokenFlag(token string) (string, bool) {
	token = strings.TrimRight(token, ",.:;)")
	if len(token) < 2 || token[0] != '-' || token == "--" || (token[1] >= '0' && token[1] <= '9') {
		return "", false
	}
	if name, ok := strings.CutPrefix(token, "--"); ok {
		name, _, _ = strings.Cut(name, "=")
		return "--" + name, true
	}
	return token[:2], true
}

// spanFlags returns the flags in a flag span's words.
func spanFlags(tokens []string) []string {
	var flags []string
	for _, token := range tokens {
		for _, part := range strings.Split(token, "|") {
			if flag, ok := tokenFlag(part); ok {
				flags = append(flags, flag)
			}
		}
	}
	return flags
}

// lookupFlag finds a flag of command or a persistent flag of its parents.
func lookupFlag(command *cobra.Command, flag string) *pflag.Flag {
	for _, set := range []*pflag.FlagSet{command.LocalFlags(), command.InheritedFlags()} {
		if name, ok := strings.CutPrefix(flag, "--"); ok {
			if f := set.Lookup(name); f != nil {
				return f
			}
		} else if f := set.ShorthandLookup(flag[1:]); f != nil {
			return f
		}
	}
	return nil
}

// acceptsFlag reports whether command accepts flag. Cobra adds the help flag
// at run time, so it is always accepted.
func acceptsFlag(command *cobra.Command, flag string) bool {
	return flag == "--help" || flag == "-h" || lookupFlag(command, flag) != nil
}

// treeAcceptsFlag reports whether command or any of its subcommands accepts
// flag.
func treeAcceptsFlag(command *cobra.Command, flag string) bool {
	if acceptsFlag(command, flag) {
		return true
	}
	return slices.ContainsFunc(command.Commands(), func(child *cobra.Command) bool { return treeAcceptsFlag(child, flag) })
}

// childCommand returns command's subcommand named or aliased word.
func childCommand(command *cobra.Command, word string) *cobra.Command {
	for _, child := range command.Commands() {
		if child.Name() == word || child.HasAlias(word) {
			return child
		}
	}
	return nil
}

func firstAlternative(word string) string {
	first, _, _ := strings.Cut(word, "|")
	return first
}

// expandAlternatives splits words such as start|update into one token list
// per alternative. A word that mixes flags and other text, such as …|--no-ca,
// becomes its flags.
func expandAlternatives(tokens []string) [][]string {
	variants := [][]string{nil}
	for _, token := range tokens {
		parts := strings.Split(token, "|")
		if len(parts) == 1 || token == "|" {
			for i := range variants {
				variants[i] = append(variants[i], token)
			}
			continue
		}
		var flags []string
		for _, part := range parts {
			if _, ok := tokenFlag(part); ok {
				flags = append(flags, part)
			}
		}
		if len(flags) > 0 {
			for i := range variants {
				variants[i] = append(variants[i], flags...)
			}
			continue
		}
		var next [][]string
		for _, variant := range variants {
			for _, part := range parts {
				next = append(next, append(slices.Clone(variant), part))
			}
		}
		variants = next
	}
	return variants
}

// isMarasiBinary reports whether word runs marasi, such as ./dist/marasi.
func isMarasiBinary(word string) bool {
	base := path.Base(word)
	return base == "marasi" || base == "marasi.exe"
}

var shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// skipAssignments drops VAR=value prefixes.
func skipAssignments(tokens []string) []string {
	for len(tokens) > 0 && shellAssignment.MatchString(tokens[0]) {
		tokens = tokens[1:]
	}
	return tokens
}

// quotedWord stands for a quoted shell word, which is never a command or flag.
const quotedWord = "\x00quoted"

// shellSegments splits text into the words of each simple command: it splits
// at |, ||, &&, ; and $(, drops a closing ), and replaces quoted words with
// quotedWord. A | inside a word, as in start|update, is kept.
func shellSegments(text string) [][]string {
	// Read a quoted command substitution as an unquoted one.
	text = strings.NewReplacer(`"$(`, " $(", `)"`, ") ").Replace(text)
	var segments [][]string
	var words []string
	var word strings.Builder
	quoted, inWord := false, false
	endWord := func() {
		if inWord {
			w := strings.Trim(word.String(), "[]")
			w = strings.TrimRight(w, ")")
			if quoted {
				w = quotedWord
			}
			if w != "" {
				words = append(words, w)
			}
		}
		word.Reset()
		quoted, inWord = false, false
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segments = append(segments, words)
		}
		words = nil
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\'' || c == '"':
			end := strings.IndexByte(text[i+1:], c)
			if end < 0 {
				end = len(text) - i - 1
			}
			quoted, inWord = true, true
			i += end + 1
		case c == '$' && i+1 < len(text) && text[i+1] == '(':
			endSegment()
			i++
		case c == ' ' || c == '\t' || c == '\n':
			endWord()
		case c == ';' || c == '&':
			endSegment()
		case c == '|' && !inWord:
			endSegment()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	endSegment()
	return segments
}

// markdownBlock is a run of markdown lines read as one unit: a fenced block,
// a table row, a list item, a heading or a paragraph.
type markdownBlock struct {
	line     int
	text     string
	fence    bool
	tableRow bool
	language string
}

var listItemStart = regexp.MustCompile(`^\s*([-*+]|\d+\.)\s`)

func markdownBlocks(doc string) []markdownBlock {
	var blocks []markdownBlock
	var current *markdownBlock
	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flush()
			block := markdownBlock{line: i + 1, fence: true, language: strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))}
			var body []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				body = append(body, lines[i])
			}
			block.text = strings.Join(body, "\n")
			blocks = append(blocks, block)
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "|"):
			flush()
			blocks = append(blocks, markdownBlock{line: i + 1, text: strings.ReplaceAll(trimmed, `\|`, "|"), tableRow: true})
		case strings.HasPrefix(trimmed, "#") || listItemStart.MatchString(lines[i]) || current == nil:
			flush()
			current = &markdownBlock{line: i + 1, text: trimmed}
		default:
			current.text += "\n" + trimmed
		}
	}
	flush()
	return blocks
}

// codeSpan is an inline code span or a fenced command, with the line it
// starts on.
type codeSpan struct {
	line   int
	text   string
	tokens []string
}

// codeSpans returns the inline code spans in a block.
func codeSpans(block markdownBlock) []codeSpan {
	var spans []codeSpan
	text := block.text
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(text) && text[i+run] == '`' {
			run++
		}
		fence := strings.Repeat("`", run)
		end := -1
		for j := i + run; j < len(text); {
			k := strings.Index(text[j:], fence)
			if k < 0 {
				break
			}
			k += j
			after := k + run
			if after < len(text) && text[after] == '`' {
				for after < len(text) && text[after] == '`' {
					after++
				}
				j = after
				continue
			}
			end = k
			break
		}
		if end < 0 {
			i += run
			continue
		}
		content := strings.ReplaceAll(text[i+run:end], "\n", " ")
		if len(content) > 1 && content[0] == ' ' && content[len(content)-1] == ' ' {
			content = content[1 : len(content)-1]
		}
		spans = append(spans, codeSpan{line: block.line + strings.Count(text[:i], "\n"), text: content})
		i = end + run
	}
	return spans
}

// fenceCommands returns the marasi commands in a sh or bash block, with
// backslash continuations joined.
func fenceCommands(block markdownBlock) []codeSpan {
	if block.language != "sh" && block.language != "bash" {
		return nil
	}
	var commands []codeSpan
	pending, start := "", 0
	for i, line := range strings.Split(block.text, "\n") {
		trimmed := strings.TrimSpace(line)
		if pending == "" {
			start = block.line + 1 + i
		}
		pending += " " + strings.TrimSuffix(trimmed, `\`)
		if strings.HasSuffix(trimmed, `\`) {
			continue
		}
		text := strings.TrimSpace(pending)
		pending = ""
		if strings.HasPrefix(text, "#") {
			continue
		}
		for _, tokens := range shellSegments(text) {
			tokens = skipAssignments(tokens)
			if len(tokens) > 0 && isMarasiBinary(tokens[0]) {
				commands = append(commands, codeSpan{line: start, text: text, tokens: tokens[1:]})
			}
		}
	}
	return commands
}

// moduleRoot returns the directory that holds go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("\nwanted:\nthe working directory\ngot:\n%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("\nwanted:\ngo.mod above the test\ngot:\nnone")
		}
		dir = parent
	}
}
