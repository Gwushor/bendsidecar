//go:build unix

package bendsidecar_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gwushor/bendsidecar"
)

func open(t *testing.T, src string, opts ...bendsidecar.Option) *bendsidecar.Program {
	t.Helper()
	if !bendsidecar.Available() {
		t.Skip("bend not on PATH")
	}
	p, err := bendsidecar.Open(context.Background(), src, opts...)
	if err != nil {
		t.Fatalf("Open(%s): %v", src, err)
	}
	return p
}

func TestCallAndParseInts(t *testing.T) {
	p := open(t, "testdata/sum.bend", bendsidecar.Threads(2), bendsidecar.Timeout(10*time.Second))
	res, err := p.Call(context.Background())
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "42 43 44" {
		t.Fatalf("stdout = %q, want %q", got, "42 43 44")
	}
	if res.Duration <= 0 {
		t.Fatalf("duration not measured")
	}
	vals, err := p.CallInts(context.Background())
	if err != nil {
		t.Fatalf("CallInts: %v", err)
	}
	if len(vals) != 3 || vals[0] != 42 || vals[2] != 44 {
		t.Fatalf("ints = %v, want [42 43 44]", vals)
	}
	if n, err := bendsidecar.FirstInt([]byte("7 8\n")); err != nil || n != 7 {
		t.Fatalf("FirstInt = %d, %v", n, err)
	}
}

func TestBuildIsCached(t *testing.T) {
	p := open(t, "testdata/sum.bend")
	start := time.Now()
	q := open(t, "testdata/sum.bend")
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("second Open took %v, want the cached binary", el)
	}
	if p.Path() != q.Path() {
		t.Fatalf("cache paths differ: %s vs %s", p.Path(), q.Path())
	}
}

func TestTimeoutKillsTheChild(t *testing.T) {
	p := open(t, "testdata/spin.bend", bendsidecar.Timeout(50*time.Millisecond))
	start := time.Now()
	_, err := p.Call(context.Background())
	if !bendsidecar.Is(err, bendsidecar.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("timeout took %v: the child was not killed", el)
	}
}

func TestValidateRejectsOutput(t *testing.T) {
	bad := errors.New("sum must be positive")
	p := open(t, "testdata/sum.bend", bendsidecar.Timeout(10*time.Second),
		bendsidecar.Validate(func(b []byte) error {
			if strings.Contains(string(b), "42") {
				return bad
			}
			return nil
		}))
	_, err := p.Call(context.Background())
	if !bendsidecar.Is(err, bendsidecar.KindBadInput) || !errors.Is(err, bad) {
		t.Fatalf("err = %v, want KindBadInput wrapping the validator's error", err)
	}
}

func TestBrokenSourceReportsBuild(t *testing.T) {
	if !bendsidecar.Available() {
		t.Skip("bend not on PATH")
	}
	_, err := bendsidecar.Open(context.Background(), "testdata/broken.bend")
	if !bendsidecar.Is(err, bendsidecar.KindBuild) {
		t.Fatalf("err = %v, want KindBuild", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stderr") {
		t.Fatalf("build error carries no compiler message: %v", err)
	}
}

func TestMissingSourceIsBadInput(t *testing.T) {
	if !bendsidecar.Available() {
		t.Skip("bend not on PATH")
	}
	_, err := bendsidecar.Open(context.Background(), "testdata/nope.bend")
	if !bendsidecar.Is(err, bendsidecar.KindBadInput) {
		t.Fatalf("err = %v, want KindBadInput", err)
	}
}

func TestArgsReachTheKernel(t *testing.T) {
	// IO.args counts argv[0] too: no user args gives 1, three give 4.
	p := open(t, "testdata/args.bend", bendsidecar.Timeout(10*time.Second))
	if n, err := p.CallInts(context.Background()); err != nil || n[0] != 1 {
		t.Fatalf("no args: %v, %v (want 1)", n, err)
	}
	if n, err := p.CallInts(context.Background(), "a", "b", "c"); err != nil || n[0] != 4 {
		t.Fatalf("three args: %v, %v (want 4)", n, err)
	}
}

func TestCallFilesCarriesPayload(t *testing.T) {
	p := scriptProgram(t, "testdata/length.sh")
	payload := make([]byte, 1<<20) // 1 MiB of binary noise, well past ARG_MAX
	for i := range payload {
		payload[i] = byte(i*7 + 13)
	}
	out, err := p.CallFiles(context.Background(), payload)
	if err != nil {
		t.Fatalf("CallFiles: %v", err)
	}
	if got, want := strings.TrimSpace(string(out)), fmt.Sprint(len(payload)); got != want {
		t.Fatalf("out.bin = %q, want %q", got, want)
	}
}

func TestCallFilesRejectsSilentKernel(t *testing.T) {
	p := scriptProgram(t, "testdata/nothing.sh")
	_, err := p.CallFiles(context.Background(), []byte("x"))
	if !bendsidecar.Is(err, bendsidecar.KindBadInput) {
		t.Fatalf("err = %v, want KindBadInput for a kernel that writes no out.bin", err)
	}
}

// scriptProgram wraps a shell script as the built binary, so the transport can be
// tested without a Bend build on every run.
func scriptProgram(t *testing.T, script string) *bendsidecar.Program {
	t.Helper()
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := bendsidecar.Open(context.Background(), "testdata/sum.bend",
		bendsidecar.Binary(abs), bendsidecar.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("Open(%s): %v", script, err)
	}
	return p
}

func TestVerdictNeedsTheKernel(t *testing.T) {
	p := open(t, "testdata/sum.bend")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err := p.Verdict(ctx)
	if err == nil {
		return // the proved kernel is installed and the file checks clean
	}
	msg := err.Error()
	if strings.Contains(msg, "BendTT") || strings.Contains(msg, "kernel") || strings.Contains(msg, "lean") {
		t.Skipf("proved kernel not installed here: %v", msg)
	}
	t.Fatalf("Verdict: %v", err)
}
