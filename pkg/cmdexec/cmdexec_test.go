package cmdexec

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInheritedPipeWaitIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	command := CommandContext(t.Context(), "/bin/sh", "-c", "sleep 30 & echo $!")
	if command.WaitDelay != 5*time.Second {
		t.Fatal("production pipe-drain bound changed")
	}
	command.WaitDelay = 50 * time.Millisecond
	start := time.Now()
	output, err := command.CombinedOutput()
	killFixtureChild(t, output)
	if !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("inherited pipe error: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("helper kept output pipe open beyond wait bound")
	}
}

func TestCancellationPreservesCauseAndExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	command := CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & echo $!; wait")
	command.WaitDelay = 50 * time.Millisecond
	output, err := command.CombinedOutput()
	killFixtureChild(t, output)
	var exitErr *ExitError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &exitErr) {
		t.Fatalf("lost cancellation or exit cause: %v", err)
	}
}

func killFixtureChild(t *testing.T, output []byte) {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("missing fixture child PID: %q", output)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = process.Kill()
}

func TestProductionUsesBoundedExecutor(t *testing.T) {
	for _, dir := range []string{"../driver", "../mount"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatalf("no production sources found under %s", dir)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				if imp.Path.Value == `"os/exec"` {
					t.Errorf("%s bypasses bounded executor", path)
				}
			}
		}
	}
}
