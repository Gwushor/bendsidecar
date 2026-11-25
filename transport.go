//go:build unix

package bendsidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// File names the child sees inside its working directory.
const (
	InFile  = "in.bin"
	OutFile = "out.bin"
)

// CallFiles is the transport for payloads that do not fit in argv. It creates a
// fresh directory, writes in to InFile, runs the program with that directory as
// its working directory and with InFile and OutFile as the first two arguments,
// then returns what the program left in OutFile.
//
// In Bend this is File.open/read/write (Base has them: File.open, File.read,
// File.read_line, File.write, File.size, File.close), driven by
// IO.pass(File, File.open(InFile, "rb")). Reading threads the handle back
// (File.read answers File & Result<…>), so a kernel that needs several chunks is
// usually written as a helper that matches on the Result.
//
// A program that writes nothing to OutFile is a bug in the kernel, not an empty
// answer: CallFiles reports KindBadInput rather than returning zero bytes.
func (p *Program) CallFiles(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bendsidecar-")
	if err != nil {
		return nil, &Error{Kind: KindBadInput, Op: "callfiles", Err: err}
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, InFile), in, 0o600); err != nil {
		return nil, &Error{Kind: KindBadInput, Op: "callfiles", Path: dir, Err: err}
	}
	full := make([]string, 0, len(args)+2)
	full = append(full, InFile, OutFile)
	full = append(full, args...)

	res, err := p.call(ctx, dir, full)
	if err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(dir, OutFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &Error{Kind: KindBadInput, Op: "callfiles", Path: p.src, Exit: res.Exit,
				Err:    errors.New("the program wrote no " + OutFile),
				Stderr: string(res.Stderr)}
		}
		return nil, &Error{Kind: KindBadInput, Op: "callfiles", Path: dir, Err: err}
	}
	return out, nil
}
