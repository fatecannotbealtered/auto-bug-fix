package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const (
	exitGeneric       = 1
	exitUsage         = 2
	exitConfig        = 4
	exitConfirmNeeded = 5
	exitConflict      = 6
	exitNetwork       = 7
	exitInterrupted   = 130
)

type outputOptions struct {
	Format  string
	Compact bool
	Fields  []string
	Quiet   bool
}

var activeOutput = outputOptions{Format: "json"}

func parseOutputArgs(args []string) (outputOptions, []string, error) {
	opts := outputOptions{Format: "json"}
	clean := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			opts.Format = "json"
		case a == "--compact":
			opts.Compact = true
		case a == "--quiet":
			opts.Quiet = true
		case a == "--format":
			if i+1 >= len(args) {
				return opts, clean, errors.New("--format requires json, text, or raw")
			}
			format := args[i+1]
			if !validFormat(format) {
				return opts, clean, fmt.Errorf("unsupported --format %q", format)
			}
			opts.Format = format
			i++
		case strings.HasPrefix(a, "--format="):
			format := strings.TrimPrefix(a, "--format=")
			if !validFormat(format) {
				return opts, clean, fmt.Errorf("unsupported --format %q", format)
			}
			opts.Format = format
		case a == "--fields":
			if i+1 >= len(args) {
				return opts, clean, errors.New("--fields requires a comma-separated field list")
			}
			opts.Fields = parseFields(args[i+1])
			i++
		case strings.HasPrefix(a, "--fields="):
			opts.Fields = parseFields(strings.TrimPrefix(a, "--fields="))
		default:
			clean = append(clean, a)
		}
	}
	return opts, clean, nil
}

func validFormat(format string) bool {
	switch format {
	case "json", "text", "raw":
		return true
	default:
		return false
	}
}

func parseFields(value string) []string {
	var out []string
	for _, f := range strings.Split(value, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func setOutputFromArgs(args []string) []string {
	opts, clean, err := parseOutputArgs(args)
	if err != nil {
		fail(exitUsage, "E_USAGE", err.Error(), nil, false)
	}
	activeOutput = opts
	return clean
}

func wantsText() bool {
	return activeOutput.Format == "text" || activeOutput.Format == "raw"
}

func printJSON(v any) {
	printJSONEnvelope(true, v, nil)
}

func printJSONEnvelope(ok bool, data any, errObj *jsonError) {
	if ok && len(activeOutput.Fields) > 0 {
		data = filterFields(data, activeOutput.Fields)
	}
	meta := map[string]any{"duration_ms": time.Since(commandStartedAt).Milliseconds()}
	// meta.notices is the read-only cache view of an available update, attached to
	// EVERY command's meta (CLI-SPEC §3, §14). It reads only the local update
	// cache — no network I/O — and is omitted when the cache has nothing to
	// report. The active/fresh view stays in data.notices on context/doctor/update.
	if notices := readUpdateNotices(); len(notices) > 0 {
		meta["notices"] = notices
	}
	out := jsonEnvelope{
		OK:            ok,
		SchemaVersion: schemaVersion,
		Data:          data,
		Error:         errObj,
		Meta:          meta,
	}
	var (
		b   []byte
		err error
	)
	if activeOutput.Compact {
		b, err = json.Marshal(out)
	} else {
		b, err = json.MarshalIndent(out, "", "  ")
	}
	if err != nil {
		log.Fatalf("marshal json: %v", err)
	}
	fmt.Println(string(b))
}

func filterFields(data any, fields []string) any {
	raw, err := json.Marshal(data)
	if err != nil {
		return data
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return data
	}
	return filterAny(decoded, fields)
}

func filterAny(v any, fields []string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, f := range fields {
			copyField(out, x, strings.Split(f, "."))
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, filterAny(item, fields))
		}
		return out
	default:
		return v
	}
}

func copyField(dst, src map[string]any, path []string) {
	if len(path) == 0 || path[0] == "" {
		return
	}
	value, ok := src[path[0]]
	if !ok {
		return
	}
	if len(path) == 1 {
		dst[path[0]] = value
		return
	}
	srcChild, ok := value.(map[string]any)
	if !ok {
		return
	}
	dstChild, _ := dst[path[0]].(map[string]any)
	if dstChild == nil {
		dstChild = map[string]any{}
		dst[path[0]] = dstChild
	}
	copyField(dstChild, srcChild, path[1:])
}

func fail(exitCode int, code, message string, details map[string]any, retryable bool) {
	if wantsText() {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(exitCode)
	}
	printJSONEnvelope(false, nil, &jsonError{
		Code:      code,
		Message:   message,
		Details:   details,
		Retryable: retryable,
	})
	os.Exit(exitCode)
}

// codedError carries an explicit taxonomy code so a failure is classified by
// type — never by sniffing human-readable message text (CLI-SPEC §6). Wrap an
// error with newCodedError at the site that knows what kind of failure it is.
type codedError struct {
	code      string
	exitCode  int
	retryable bool
	err       error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func newCodedError(code string, exitCode int, retryable bool, err error) error {
	return &codedError{code: code, exitCode: exitCode, retryable: retryable, err: err}
}

// exitCodeForError is the single place that maps an error to (exit code, code,
// retryable). It classifies by type: an explicit codedError, or a typed
// updateHTTPError mapped by HTTP status. Anything else is a generic runtime
// error — we never guess intent from the message string.
func exitCodeForError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.exitCode, ce.code, ce.retryable
	}
	var httpErr *updateHTTPError
	if errors.As(err, &httpErr) {
		return statusToCode(httpErr.StatusCode)
	}
	return exitGeneric, "E_RUNTIME", false
}

// statusToCode maps an upstream HTTP status onto the error taxonomy. Centralized
// so the one real HTTP path (the npm registry check) classifies by status code,
// not by parsing a human-readable error string.
func statusToCode(status int) (int, string, bool) {
	switch {
	case status == 404:
		return 3, "E_NOT_FOUND", false
	case status == 408:
		return 8, "E_TIMEOUT", true
	case status == 429, status >= 500:
		return exitNetwork, "E_NETWORK", true
	default:
		return exitGeneric, "E_RUNTIME", false
	}
}

func failErr(prefix string, err error) {
	exitCode, code, retryable := exitCodeForError(err)
	msg := err.Error()
	if prefix != "" {
		msg = prefix + ": " + msg
	}
	fail(exitCode, code, msg, nil, retryable)
}
