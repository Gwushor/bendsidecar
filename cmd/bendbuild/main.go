// Command bendbuild precompiles Bend programs and writes them with a manifest, so a
// deployment can ship binaries instead of a compiler.
//
//	bendbuild -src . -out dist
//
// Each program lands at <out>/<name>-<hash8>, where the hash covers the source's
// bytes: the library looks the file up by the same name and never compiles at
// runtime. manifest.json records the compiler version and every hash, which is
// what a reviewer reads to know what shipped.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type entry struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Binary  string `json:"binary"`
	Hash    string `json:"hash"`
	Bytes   int64  `json:"bytes"`
	BuiltAt string `json:"built_at"`
}

type manifest struct {
	Bend     string  `json:"bend"`
	Programs []entry `json:"programs"`
}

func main() {
	src := flag.String("src", ".", "directory to scan for .bend files")
	out := flag.String("out", "dist", "output directory")
	flag.Parse()

	if err := run(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "bendbuild:", err)
		os.Exit(1)
	}
}

func run(src, out string) error {
	bend, err := exec.LookPath("bend")
	if err != nil {
		return fmt.Errorf("bend not on PATH: %w", err)
	}
	version, err := exec.Command(bend, "version").Output()
	if err != nil {
		return fmt.Errorf("bend version: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	files, err := filepath.Glob(filepath.Join(src, "*.bend"))
	if err != nil {
		return err
	}
	if explicit := flag.Args(); len(explicit) > 0 {
		files = explicit
	}
	if len(files) == 0 {
		return fmt.Errorf("no .bend files in %s", src)
	}
	sort.Strings(files)

	m := manifest{Bend: strings.TrimSpace(string(version))}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".bend")
		if name == "LAWS" || name == "PROOF" || name == "Base" {
			continue // law pair and the stdlib are not sidecars
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:8])
		bin := filepath.Join(out, name+"-"+hash)

		if st, err := os.Stat(bin); err == nil && st.Size() > 0 {
			m.Programs = append(m.Programs, entry{Name: name, Source: f, Binary: bin,
				Hash: hash, Bytes: st.Size(), BuiltAt: st.ModTime().UTC().Format("2006-01-02T15:04:05Z")})
			fmt.Printf("cached  %-16s %s\n", name, bin)
			continue
		}
		tmp := bin + ".tmp"
		cmd := exec.Command(bend, f, "-o", tmp)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("build %s: %w", f, err)
		}
		if err := os.Rename(tmp, bin); err != nil {
			os.Remove(tmp)
			return err
		}
		st, _ := os.Stat(bin)
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		m.Programs = append(m.Programs, entry{Name: name, Source: f, Binary: bin,
			Hash: hash, Bytes: size, BuiltAt: st.ModTime().UTC().Format("2006-01-02T15:04:05Z")})
		fmt.Printf("built   %-16s %s (%d B)\n", name, bin, size)
	}

	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "manifest.json"), append(blob, '\n'), 0o644)
}
