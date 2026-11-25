// Package bendsidecar runs Bend 2 programs as isolated child processes from Go.
//
// Bend 2 compiles a whole program to one C file and runs it as a program, not as
// a library: there is no callable entry point, every run leaks the runtime's
// stacks (100 calls grow RSS by ~400 MB), and a failure inside Bend is an abort()
// that would kill the host process. So the only sound way to use a Bend program
// from Go is a sidecar: one process per call, plain argv in, plain bytes out.
//
// # The contract
//
// A Bend program is expected to read its input from argv (IO.args) and write its
// answer to stdout (IO.print). Base has no file I/O yet, so large payloads can
// only travel as arguments (ARG_MAX, ~2 MB) or through the program's own foreign
// effects — in which case pass a file path in argv and document it on the program.
//
// # What is NOT guaranteed
//
// LAWS.bend guarantees what the checker models, and the C boundary, the argv it
// received and the bytes it printed are outside that model. Validate the output
// on the Go side (Option Validate); never trust it because "it was proved".
//
// # Limits
//
//   - One process per call. Never reuse; the runtime keeps state and grows.
//   - ulimit -v is useless: Bend reserves terabytes of address space up front.
//     Cap memory with a cgroup or a systemd slice if you need a ceiling.
//   - No secrets in argv: they are visible in /proc to every local user.
//
// Example:
//
//	p, err := bendsidecar.Open(ctx, "risk.bend", bendsidecar.Threads(4),
//		bendsidecar.Timeout(2*time.Second))
//	if err != nil {
//		return err
//	}
//	res, err := p.Call(ctx, "1500", "300")   // the program's own arguments
//	if err != nil {
//		return err
//	}
//	n, err := bendsidecar.FirstInt(res.Stdout)
package bendsidecar
