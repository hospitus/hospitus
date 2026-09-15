package cmdutil_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryCommandParsesOutputThroughOneHelper guards the drift
// ParseOutputFormat was written to end.
//
// Hand-written `if outputFmt == "json"` accepts anything else in silence, so
// "--output yaml" printed a table and a script parsing the result got a header
// row. The helper refuses an unknown value instead. Fourteen commands were
// still mapping it by hand after the helper landed — a count nobody had,
// because nothing related the copies to each other.
func TestEveryCommandParsesOutputThroughOneHelper(t *testing.T) {
	root := filepath.Join("..", "..", "cmd")

	var handRolled []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(content), "\n") {
			// The comparison itself, not the word "json": a struct tag or a
			// Content-Type check is not what this is about.
			if strings.Contains(line, `== "json"`) {
				handRolled = append(handRolled, filepath.ToSlash(path)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for _, where := range handRolled {
		t.Errorf("--output is compared by hand, so an unknown value falls through "+
			"to the table instead of being refused; use cmdutil.OutputFormatFrom:\n  %s", where)
	}
}

// TestOutputFlagIsReadWhereverItIsRegistered is the other half of the same
// drift.
//
// A command that calls AddOutputFlag and never reads the value accepts
// "--output json" and prints the table anyway — no comparison to find, so the
// test above cannot see it. That is how "bhyve expose list", "jail volume
// list" and "qemu expose list" shipped a flag that did nothing.
//
// Per registration, not per file: one file often declares a list command and
// an info command, and a file-wide search called the pair covered when only
// one of them read the flag. Any spelling of the read counts — the constant
// or the literal — because reporting "never reads it" about a file that reads
// it by literal name is a wrong reason, and a wrong reason sends the reader
// looking for a defect that is not there.
func TestOutputFlagIsReadWhereverItIsRegistered(t *testing.T) {
	root := filepath.Join("..", "..", "cmd")

	var deaf []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(content)

		// Each constructor that registers the flag, and the handler it names.
		for _, fn := range splitTopLevelFuncs(source) {
			if !strings.Contains(fn.body, "AddOutputFlag(") {
				continue
			}
			handler := runEHandler(fn.body)
			if handler == "" {
				// Registered on a parent command for its subcommands to use:
				// the subcommands are checked on their own.
				continue
			}
			target := fn.body
			if body := funcBody(source, handler); body != "" {
				target = body
			}
			if !readsOutputFlag(target) {
				deaf = append(deaf, filepath.ToSlash(path)+": "+handler)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for _, where := range deaf {
		t.Errorf("%s registers --output and never reads it, so the flag is accepted "+
			"and discarded; read it with cmdutil.OutputFormatFrom", where)
	}
}

func readsOutputFlag(body string) bool {
	return strings.Contains(body, "OutputFormatFrom(") ||
		strings.Contains(body, "ParseOutputFormat(") ||
		strings.Contains(body, "FlagOutput)") ||
		strings.Contains(body, `GetString("output")`)
}

type topLevelFunc struct {
	name string
	body string
}

// splitTopLevelFuncs returns each function declaration's own source.
//
// Parsed rather than split on "\nfunc ": that bounded every body at the *next*
// declaration, which is right in the middle of a file and wrong at the end,
// where the last function swallowed every trailing const, var and type. The
// parser gives each declaration its real extent.
func splitTopLevelFuncs(source string) []topLevelFunc {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", source, 0)
	if err != nil {
		// A file this cannot parse is one `go build` will reject anyway; the
		// check has nothing useful to say about it.
		return nil
	}

	var out []topLevelFunc
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		lo := fset.Position(fn.Pos()).Offset
		hi := fset.Position(fn.End()).Offset
		if lo < 0 || hi > len(source) || lo >= hi {
			continue
		}
		out = append(out, topLevelFunc{name: fn.Name.Name, body: source[lo:hi]})
	}
	return out
}

// runEHandler reports the function named by "RunE: <name>", or "" when the
// command uses an inline closure — which the constructor's own body covers.
func runEHandler(body string) string {
	i := strings.Index(body, "RunE:")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(body[i+len("RunE:"):])
	if strings.HasPrefix(rest, "func") {
		return ""
	}
	name := rest
	if j := strings.IndexAny(name, ",\n}"); j >= 0 {
		name = name[:j]
	}
	return strings.TrimSpace(name)
}

func funcBody(source, name string) string {
	for _, fn := range splitTopLevelFuncs(source) {
		if fn.name == name {
			return fn.body
		}
	}
	return ""
}

// TestDriftCheckReadsOneHandlerAtATime pins the behavior the check depends on:
// two commands in one file, only the second of which reads --output.
//
// A file-wide search called that pair covered. It is also the case a
// body-boundary bug would silently break, by letting the later handler's read
// count for the earlier one.
func TestDriftCheckReadsOneHandlerAtATime(t *testing.T) {
	const source = `package demo

func newAlphaCommand() *cobra.Command {
	cmd := &cobra.Command{RunE: runAlpha}
	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runAlpha(cmd *cobra.Command, args []string) error {
	return nil
}

func newBetaCommand() *cobra.Command {
	cmd := &cobra.Command{RunE: runBeta}
	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runBeta(cmd *cobra.Command, args []string) error {
	format, err := cmdutil.OutputFormatFrom(cmd)
	_ = format
	return err
}

const trailing = "OutputFormatFrom( in a declaration after the last function"
`

	if got := readsOutputFlag(funcBody(source, "runAlpha")); got {
		t.Error("runAlpha does not read --output, but the check says it does")
	}
	if got := readsOutputFlag(funcBody(source, "runBeta")); !got {
		t.Error("runBeta reads --output, but the check says it does not")
	}

	// The trailing declaration must not be swallowed by the last function.
	if strings.Contains(funcBody(source, "runBeta"), "trailing") {
		t.Error("the last function's body ran past its closing brace")
	}

	for _, fn := range splitTopLevelFuncs(source) {
		if fn.name == "newAlphaCommand" && runEHandler(fn.body) != "runAlpha" {
			t.Errorf("RunE handler for newAlphaCommand = %q, want runAlpha", runEHandler(fn.body))
		}
	}
}
