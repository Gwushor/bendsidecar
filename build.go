//go:build unix

package bendsidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultBuildTimeout = 5 * time.Minute

// BuildTimeout bounds one compile of the .bend file.
func BuildTimeout(d time.Duration) Option {
	return func(p *Program) { p.buildTimeout = d }
}

// Available reports whether the bend compiler can be found on PATH.
func Available() bool {
	_, err := exec.LookPath("bend")
	return err == nil
}

// build compiles the source into the cache and returns the executable's path.
// The cache key is the file's bytes plus the compiler's version: an edited source
// or an upgraded bend rebuilds, and a fifty-second compile happens once.
func (p *Program) build(ctx context.Context) (string, error) {
	bend, err := p.bendExe()
	if err != nil {
		return "", err
	}
	src, err := os.ReadFile(p.src)
	if err != nil {
		return "", &Error{Kind: KindBadInput, Op: "read", Path: p.src, Err: err}
	}
	dir, err := cacheDir()
	if err != nil {
		return "", &Error{Kind: KindBuild, Op: "cache", Err: err}
	}
	sum := sha256.Sum256(append(src, []byte("\x00bend:"+bendVersion(bend))...))
	out := filepath.Join(dir, strings.TrimSuffix(filepath.Base(p.src), ".bend")+"-"+hex.EncodeToString(sum[:8]))

	if st, err := os.Stat(out); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		return out, nil
	}

	timeout := p.buildTimeout
	if timeout == 0 {
		timeout = defaultBuildTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tmp := out + ".tmp"
	cmd := exec.CommandContext(cctx, bend, p.src, "-o", tmp)
	cmd.Env = p.childEnv()
	errBuf := &tailBuffer{limit: p.stderrLimit}
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = errBuf
	runErr := cmd.Run()
	if runErr != nil {
		os.Remove(tmp)
		kind := KindBuild
		if cctx.Err() != nil {
			kind = KindTimeout
		}
		return "", &Error{Kind: kind, Op: "build", Path: p.src, Err: runErr,
			Stderr: errBuf.String() + outBuf.String()}
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return "", &Error{Kind: KindBuild, Op: "build", Path: p.src, Err: err}
	}
	return out, nil
}

func cacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "bendsidecar")
	return dir, os.MkdirAll(dir, 0o755)
}

// bendVersion asks the compiler for its version, so a compiler upgrade
// invalidates the cache instead of silently keeping old binaries.
func bendVersion(bend string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bend, "version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// ParseInts parses whitespace-separated integers, the cheapest way for a Bend
// kernel to answer.
func ParseInts(b []byte) ([]int64, error) {
	fields := strings.Fields(string(b))
	vals := make([]int64, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, &Error{Kind: KindBadInput, Op: "parse", Err: err,
				Stderr: "output was not integers: " + truncate(string(b), 200)}
		}
		vals = append(vals, v)
	}
	return vals, nil
}

// FirstInt returns the first integer in the output, for a one-answer kernel.
func FirstInt(b []byte) (int64, error) {
	vals, err := ParseInts(b)
	if err != nil {
		return 0, err
	}
	if len(vals) == 0 {
		return 0, &Error{Kind: KindBadInput, Op: "parse", Err: errors.New("no integers in output")}
	}
	return vals[0], nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
