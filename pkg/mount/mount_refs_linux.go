//go:build linux

package mount

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GetBindMountRefs finds other mounts of the exact staged directory or device
// file in the host namespace. Matching both device number and filesystem root
// avoids conflating distinct NFS subdirectories or raw devices on /dev's tmpfs.
func GetBindMountRefs(ctx context.Context, sourcePath string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve staged source: %w", err)
	}
	file, err := os.Open("/proc/1/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("failed to read host mount information: %w", err)
	}
	refs, parseErr := bindMountRefs(filepath.Clean(source), file)
	closeErr := file.Close()
	if parseErr != nil {
		return nil, parseErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close host mount information: %w", closeErr)
	}
	return refs, nil
}

func bindMountRefs(source string, reader io.Reader) ([]string, error) {
	type entry struct{ device, root, target string }
	var entries []entry
	var containing *entry
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " - ", 2)
		if len(parts) != 2 {
			return nil, errMalformedMountInfo
		}
		filesystem := strings.Fields(parts[1])
		if len(filesystem) < 3 {
			return nil, errMalformedMountInfo
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 6 {
			return nil, errMalformedMountInfo
		}
		numbers := strings.Split(fields[2], ":")
		if len(numbers) != 2 {
			return nil, errMalformedMountInfo
		}
		for _, number := range numbers {
			if _, err := strconv.ParseUint(number, 10, 32); err != nil {
				return nil, errMalformedMountInfo
			}
		}
		e := entry{device: fields[2], root: unescape.Replace(fields[3]), target: unescape.Replace(fields[4])}
		// Namespace bind mounts have roots such as net:[4026532506]. They
		// are valid host mount entries, but cannot match a storage volume's
		// absolute filesystem root. Other non-path roots still fail closed.
		if !filepath.IsAbs(e.target) || (!filepath.IsAbs(e.root) && filesystem[0] != "nsfs") {
			return nil, errMalformedMountInfo
		}
		entries = append(entries, e)
		if source == e.target || strings.HasPrefix(source, strings.TrimSuffix(e.target, "/")+"/") {
			if containing == nil || len(e.target) >= len(containing.target) {
				candidate := e
				containing = &candidate
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan host mount information: %w", err)
	}
	if containing == nil {
		return nil, fmt.Errorf("%w: no mount contains staged source", errMalformedMountInfo)
	}
	if !filepath.IsAbs(containing.root) {
		return nil, fmt.Errorf("%w: staged source is a namespace handle, not a storage volume", errMalformedMountInfo)
	}
	relative, err := filepath.Rel(containing.target, source)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(containing.root, relative)
	var refs []string
	for _, e := range entries {
		if e.device == containing.device && e.root == root && e.target != source {
			refs = append(refs, e.target)
		}
	}
	return refs, nil
}
