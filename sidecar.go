//go:build unix

package bendsidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Kind classifies a failure, so a caller can decide what to do about it.
type Kind string

const (
	KindNoBend   Kind = "no_bend"   // the bend compiler is not on PATH
	KindBuild    Kind = "build"     // the .bend file did not compile
	KindTimeout  Kind = "timeout"   // the call exceeded its timeout
	KindCrash    Kind = "crash"     // the child died on a signal (Bend's abort())
	KindExit     Kind = "exit"      // the child exited non-zero itself
	KindBadInput Kind = "bad_input" // Open was given something unusable
)

// Error is every failure this package returns. Stderr holds the tail of what the
// child wrote, which for Bend is the compiler's message or the abort's line.
type Error struct {
	Kind   Kind
	Op     string
	Path   string
	Exit   int
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("bendsidecar: ")
	b.WriteString(string(e.Kind))
	if e.Op != "" {
		b.WriteString(" ")
		b.WriteString(e.Op)
	}
	if e.Path != "" {
		b.WriteString(" ")
		b.WriteString(e.Path)
	}
	if e.Exit != 0 {
		fmt.Fprintf(&b, " exit=%d", e.Exit)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	if e.Stderr != "" {
		b.WriteString("\n  stderr: ")
		b.WriteString(strings.TrimSpace(e.Stderr))
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is reports whether err is a *Error of the given kind.
func Is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

// Result is what one call produced.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	Exit     int
	Duration time.Duration
}

// Option configures a Program.
type Option func(*Program)

// Threads sets the C runtime's thread count (+RTS --threads N). Zero leaves the
// runtime's own default.
func Threads(n int) Option { return func(p *Program) { p.threads = n } }

// Timeout bounds one call. Zero means no bound beyond the caller's context.
func Timeout(d time.Duration) Option { return func(p *Program) { p.timeout = d } }

// MaxParallel caps how many calls of this Program may run at once. Zero means
// no cap; every extra call is another process.
func MaxParallel(n int) Option {
	return func(p *Program) {
		if n > 0 {
			p.sem = make(chan struct{}, n)
		}
	}
}

// Validate runs on the child's stdout before Call returns it. Use it for every
// output that feeds money, state or anything outside the Bend model.
func Validate(f func(stdout []byte) error) Option {
	return func(p *Program) { p.validate = f }
}

// Binary uses an already built executable and skips the build and its cache.
func Binary(path string) Option { return func(p *Program) { p.bin = path } }

// BendPath names the bend compiler when it is not on PATH.
func BendPath(path string) Option { return func(p *Program) { p.bend = path } }

// Env adds variables to the child's environment. The child never inherits the
// parent's environment, so a secret does not leak into the sidecar by accident.
func Env(kv ...string) Option { return func(p *Program) { p.env = append(p.env, kv...) } }

// StderrLimit bounds how much of the child's stderr is kept for the error
// message; the default keeps the last 8 KiB.
func StderrLimit(n int) Option { return func(p *Program) { p.stderrLimit = n } }

// Program is one Bend file, built and ready to run.
type Program struct {
	src             string
	bin             string
	bend            string
	threads         int
	timeout         time.Duration
	buildTimeout    time.Duration
	env             []string
	validate        func([]byte) error
	stderrLimit     int
	prebuiltDir     string
	requirePrebuilt bool
	sem             chan struct{}
}

// Open checks the source, builds it if the cached binary is stale, and returns a
// Program. Building is cached by the file's contents and the compiler version, so
// repeated Opens are cheap and a fifty-second build happens once.
func Open(ctx context.Context, src string, opts ...Option) (*Program, error) {
	p := &Program{src: src, bend: "bend", stderrLimit: 8 << 10}
	for _, o := range opts {
		o(p)
	}
	abs, err := resolveSource(src)
	if err != nil {
		return nil, err
	}
	p.src = abs
	if p.bin != "" {
		if _, err := os.Stat(p.bin); err != nil {
			return nil, &Error{Kind: KindBadInput, Op: "open", Path: p.bin, Err: err}
		}
		return p, nil
	}
	bin, err := p.build(ctx)
	if err != nil {
		return nil, err
	}
	p.bin = bin
	return p, nil
}

func resolveSource(src string) (string, error) {
	abs, err := os.Stat(src)
	if err != nil {
		return "", &Error{Kind: KindBadInput, Op: "open", Path: src, Err: err}
	}
	if abs.IsDir() {
		return "", &Error{Kind: KindBadInput, Op: "open", Path: src, Err: errors.New("is a directory")}
	}
	if !strings.HasSuffix(src, ".bend") {
		return "", &Error{Kind: KindBadInput, Op: "open", Path: src, Err: errors.New("want a .bend file")}
	}
	full, err := filepath.Abs(src)
	if err != nil {
		return "", &Error{Kind: KindBadInput, Op: "open", Path: src, Err: err}
	}
	return full, nil
}

// Call runs the program once with these arguments and returns what it printed.
func (p *Program) Call(ctx context.Context, args ...string) (Result, error) {
	return p.call(ctx, "", args)
}

// call is Call with a working directory: empty means inherit the host's.
func (p *Program) call(ctx context.Context, dir string, args []string) (Result, error) {
	if p.sem != nil {
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return Result{}, &Error{Kind: KindTimeout, Op: "call", Path: p.src, Err: ctx.Err()}
		}
	}
	cctx := ctx
	var cancel context.CancelFunc
	if p.timeout > 0 {
		cctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	argv := make([]string, 0, len(args)+4)
	if p.threads > 0 {
		argv = append(argv, "+RTS", "--threads", strconv.Itoa(p.threads), "-RTS")
	}
	argv = append(argv, args...)

	cmd := exec.CommandContext(cctx, p.bin, argv...)
	cmd.Dir = dir
	cmd.Env = p.childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	errBuf := &tailBuffer{limit: p.stderrLimit}
	cmd.Stdout = &out
	cmd.Stderr = errBuf
	// The Bend runtime starts a thread pool; killing only the parent leaves it.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errBuf.Bytes(), Duration: time.Since(start)}

	if cctx.Err() == context.DeadlineExceeded || cctx.Err() == context.Canceled {
		return res, &Error{Kind: KindTimeout, Op: "call", Path: p.src, Err: cctx.Err(), Stderr: string(res.Stderr)}
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.Exit = ee.ExitCode()
			kind := KindExit
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				kind = KindCrash // Bend's abort() on a guard page or a failed check
			}
			return res, &Error{Kind: kind, Op: "call", Path: p.src, Exit: res.Exit, Err: runErr, Stderr: string(res.Stderr)}
		}
		return res, &Error{Kind: KindExit, Op: "call", Path: p.src, Err: runErr, Stderr: string(res.Stderr)}
	}
	if p.validate != nil {
		if err := p.validate(res.Stdout); err != nil {
			return res, &Error{Kind: KindBadInput, Op: "validate", Path: p.src, Err: err, Stderr: string(res.Stderr)}
		}
	}
	return res, nil
}

// CallInts runs the program and parses its stdout as whitespace-separated
// integers — the shortest useful contract between a Bend kernel and Go.
func (p *Program) CallInts(ctx context.Context, args ...string) ([]int64, error) {
	res, err := p.Call(ctx, args...)
	if err != nil {
		return nil, err
	}
	return ParseInts(res.Stdout)
}

// Verdict rechecks the source with Bend's proven kernel and reports whether every
// proof still closes. It needs the BendTT kernel: `bend --verdict` looks for Lean
// v4.34.0 through elan (or a built kernel named by $BENDTT) and fails without it,
// so gate this behind an optional CI step rather than a hot path. Run it before a
// sidecar binary ships.
func (p *Program) Verdict(ctx context.Context) error {
	bend, err := p.bendExe()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bend, p.src, "--verdict")
	cmd.Env = p.childEnv()
	var out bytes.Buffer
	errBuf := &tailBuffer{limit: p.stderrLimit}
	cmd.Stdout = &out
	cmd.Stderr = errBuf
	if err := cmd.Run(); err != nil {
		return &Error{Kind: KindBuild, Op: "verdict", Path: p.src, Err: err, Stderr: errBuf.String() + out.String()}
	}
	if !strings.Contains(out.String()+errBuf.String(), "ALL PROOFS CHECK") {
		return &Error{Kind: KindBuild, Op: "verdict", Path: p.src,
			Err: errors.New("proofs did not all check"), Stderr: out.String() + errBuf.String()}
	}
	return nil
}

// Path returns the built executable.
func (p *Program) Path() string { return p.bin }

// childEnv is deliberately small: PATH to find nothing, HOME for the runtime's own
// caches, BENDRTS off (threads travel as arguments), and whatever the caller opted
// into. Nothing from the parent leaks in, secrets included.
func (p *Program) childEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	return append(env, p.env...)
}

func (p *Program) bendExe() (string, error) {
	if p.bend == "" {
		p.bend = "bend"
	}
	path, err := exec.LookPath(p.bend)
	if err != nil {
		return "", &Error{Kind: KindNoBend, Op: "lookpath", Path: p.bend, Err: err,
			Stderr: "install Bend 2 (curl -fsSL https://bend-lang.com/install.sh | sh)"}
	}
	return path, nil
}

// tailBuffer keeps the last limit bytes written to it, so a chatty child cannot
// grow the host's memory.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

func (t *tailBuffer) String() string { return string(t.Bytes()) }
