package clicontract

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// effectiveArgv is the argv cobra parses for one invocation: the caller's
// argv, or the process's own arguments when the caller passed nil and
// cobra fell back to os.Args[1:].
func effectiveArgv(argv []string) []string {
	if argv == nil {
		return os.Args[1:]
	}
	return argv
}

// retryArgv decides whether the failed pass is one a single-dash long flag
// can produce, and returns the argv for the next pass. Nil means no
// rewrite: the caller reports the original error.
//
// Two failure classes retry, both of them preceding every RunE so nothing
// has run when the next pass starts:
//
//   - A flag-parse error on the command cobra resolved (recorded on exec
//     by the root's flag error func). Only pflag's NotExistError — the
//     unknown-flag and unknown-shorthand flavors a single-dash long flag
//     produces — qualifies; missing values, bad values, and positional
//     errors never do.
//   - A command-resolution failure, the class a value-taking single-dash
//     flag typed before the subcommand name produces (`-format json probe
//     tgt` reads "json" as the command). Only the root reports it:
//     cobra's Find calls legacyArgs only on a command whose Args is nil,
//     which every leaf replaces with its positional rule, and legacyArgs
//     rejects an unmatched name only on the command with no parent — the
//     root. A group below the root has nil Args and a parent, so it takes
//     the tokens as its own args and fails in its own RunE.
//
// The rewrite covers only the failing command's own segment of argv and
// uses its flag set, so tokens belonging to a deeper command are never
// touched. A pass that rewrites nothing does not re-execute.
func (a *App) retryArgv(exec *execution, root *cobra.Command, argv []string) []string {
	argv = effectiveArgv(argv)
	var failing *cobra.Command
	switch {
	case exec.flagErr != nil:
		if !isUnknownFlagErr(exec.flagErr) {
			return nil
		}
		failing = exec.flagErrCmd
	default:
		found, _, err := root.Find(argv)
		if err == nil {
			return nil
		}
		failing = found
	}
	if failing == nil {
		return nil
	}
	return rewriteSingleDash(a, failing, argv)
}

// isUnknownFlagErr reports whether the flag-parse error is pflag's
// "flag does not exist" class. pflag types its parse failures — v1.0.9
// raises NotExistError for exactly the unknown-long-flag and
// unknown-shorthand readings, and ValueRequiredError (missing value),
// InvalidValueError (bad value), and InvalidSyntaxError for the rest —
// so the type alone is the detection, no message matching. The flavor
// field is unexported; both flavors are what a single-dash long flag
// produces, so the distinction does not matter here.
func isUnknownFlagErr(err error) bool {
	var ne *pflag.NotExistError
	return errors.As(err, &ne)
}

// rewriteSingleDash rewrites the single-dash spellings of cmd's long
// flags to their double-dash form: `-agent x` becomes `--agent x`,
// `-agent=x` becomes `--agent=x`. It returns nil when no token changed.
//
// Only cmd's own segment of argv is rewritten. The segment ends at the
// first positional off cmd's command path: for a command that owns
// subcommands that positional is where cobra descends, and for a leaf it
// is the token cobra itself read as the command name, which a rewrite's
// value slot can swallow. A leaf otherwise parses the whole argv, so its
// segment runs to the end, and its NoInterspersed rule ends the flags at
// the first positional.
//
// The walk mirrors pflag's own consumption rules, read from cmd's merged
// flag set (pflag offers no non-destructive parse, so the rules are
// restated here against its exported Flag metadata): a long flag without
// "=" that takes a value consumes the next token; a shorthand cluster
// that ends in a value-taking shorthand consumes the next token; a bare
// "--" ends the flags. Value tokens and positionals are never rewritten,
// and a token whose shorthand reading resolves keeps that meaning — a
// single-dash long flag is only tried when the shorthand reading fails.
func rewriteSingleDash(a *App, cmd *cobra.Command, argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	fs := cmd.Flags()
	path := cobraPath(cmd)
	group := cmd.HasSubCommands()
	rewritten := make([]string, len(argv))
	copy(rewritten, argv)
	changed := false
	pathIdx := 0
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		switch {
		case tok == "--":
			// A bare "--" ends the flags; everything after it is
			// positional.
			return rewrittenArgv(rewritten, changed)
		case strings.HasPrefix(tok, "--"):
			// Already a long flag: only its value consumption
			// classifies the next token.
			if name, _, hasEq := strings.Cut(tok[2:], "="); !hasEq {
				if f := fs.Lookup(name); f != nil && f.NoOptDefVal == "" {
					i++
				}
			}
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			rest := tok[1:]
			consumesNext, resolves := shorthandRead(fs, rest)
			if resolves {
				// The shorthand reading parses, so the token keeps it:
				// a single-dash long flag applies only when it fails.
				if consumesNext {
					i++
				}
				continue
			}
			name, _, hasEq := strings.Cut(rest, "=")
			if f := fs.Lookup(name); f != nil {
				rewritten[i] = "--" + rest
				changed = true
				if !hasEq && f.NoOptDefVal == "" {
					i++ // the rewritten flag takes the next token as its value
				}
			}
		default:
			// A positional, or the next command name on the path.
			if pathIdx < len(path) && tok == path[pathIdx] {
				pathIdx++
				continue
			}
			// Only a non-empty token outside the dash namespace is a
			// command name to cobra's stripFlags: "-" and "" are
			// skipped there, so they neither descend nor end the
			// segment.
			commandName := tok != "" && !strings.HasPrefix(tok, "-")
			if commandName && (group || pathIdx < len(path)) {
				// The first positional off the command path ends this
				// command's segment: the positional where cobra
				// descends below a group, or the token a leaf's rewrite
				// swallowed out of the leaf's own command name.
				return rewrittenArgv(rewritten, changed)
			}
			if a.declaredNoInterspersed(path) {
				// NoInterspersed, with the command path already
				// complete: the first positional ends the flags for
				// this command.
				return rewrittenArgv(rewritten, changed)
			}
		}
	}
	return rewrittenArgv(rewritten, changed)
}

// rewrittenArgv reports the walk's result: the rewritten argv when a token
// changed, nil otherwise (nothing rewrites, nothing re-executes).
func rewrittenArgv(argv []string, changed bool) []string {
	if !changed {
		return nil
	}
	return argv
}

// shorthandRead walks one single-dash token's letters the way pflag does:
// every letter must name a declared shorthand, a value-taking one ends the
// cluster by absorbing the rest of the token (or the next token when it
// is last), and a bool-like one followed by "=" takes the rest as its
// value. It reports whether the shorthand reading parses and whether the
// next argv token is the cluster's value.
func shorthandRead(fs *pflag.FlagSet, rest string) (consumesNext, resolves bool) {
	for j := 0; j < len(rest); j++ {
		f := fs.ShorthandLookup(rest[j : j+1])
		if f == nil {
			return false, false
		}
		if f.NoOptDefVal != "" {
			// -b=true: pflag reads the rest as this bool-like
			// shorthand's value and ends the cluster.
			if len(rest)-j > 2 && rest[j+1] == '=' {
				return false, true
			}
			continue // bool-like: the next letter parses too
		}
		// Value-taking shorthand: -fvalue and -f=value absorb the rest;
		// a trailing -f takes the next token.
		return j == len(rest)-1, true
	}
	// Every letter was a bool-like shorthand: the cluster is complete
	// and self-contained.
	return false, true
}

// cobraPath is cmd's command path below the root: the names a resolved
// command contributes to argv ("remote list" for the list leaf).
func cobraPath(cmd *cobra.Command) []string {
	var path []string
	for c := cmd; c.Parent() != nil; c = c.Parent() {
		path = append(path, c.Name())
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// declaredNoInterspersed reports whether the declared command at path is
// a leaf declared NoInterspersed — the nodes whose cobra flag set parses
// without interspersing.
func (a *App) declaredNoInterspersed(path []string) bool {
	if len(path) == 0 {
		return false
	}
	cmds := a.Commands
	var c *Command
	for _, name := range path {
		found := false
		for _, candidate := range cmds {
			if candidate.Name == name {
				c, cmds, found = candidate, candidate.Subcommands, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return c != nil && len(c.Subcommands) == 0 && c.NoInterspersed
}
