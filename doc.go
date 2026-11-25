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
// Two transports, both plain bytes:
//
//   - Call: arguments in (IO.args), text out (IO.print). Numbers as
//     whitespace-separated text are the cheapest thing both sides parse. Capped by
//     ARG_MAX (~2 MB).
//   - CallFiles: the child runs in a fresh directory that holds InFile, and leaves
//     its answer in OutFile. Base does have file effects — File.open, File.read,
//     File.read_line, File.write, File.size, File.close, unwrapped with
//     IO.pass(File, File.open(InFile, "rb")) — so any payload size works.
//
// Anything Base still lacks can be added by the program itself: a Bend def whose
// body is `import "./effs/x.c"` (plus a .js twin for the dev runner) is a foreign
// effect. Bend publishes packages too (--publish / link), which is how such a
// helper travels between projects. Remember that a foreign effect has no ABI
// promise: Bend's own guide says to rebuild effects with every compiler update.
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
