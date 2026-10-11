package mount

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

var errMalformedMountInfo = errors.New("malformed mount information")

// isPathInMountInfo checks an exact mount point, not paths contained within a
// mount. Parse the entire table: unreadable or malformed input is never proof
// that a path is unmounted. No filesystem lookup touches the target itself.
func isPathInMountInfo(target string, reader io.Reader) (bool, error) {
	if target == "" {
		return false, fmt.Errorf("%w: empty mount target", errMalformedMountInfo)
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	mounted, seen := false, false
	for scanner.Scan() {
		seen = true
		before, after, ok := strings.Cut(scanner.Text(), " - ")
		fields := strings.Fields(before)
		if !ok || len(fields) < 6 || len(strings.Fields(after)) < 3 {
			return false, errMalformedMountInfo
		}
		numbers := strings.Split(fields[2], ":")
		if len(numbers) != 2 {
			return false, errMalformedMountInfo
		}
		for _, number := range numbers {
			if _, err := strconv.ParseUint(number, 10, 32); err != nil {
				return false, errMalformedMountInfo
			}
		}
		point, err := decodeMountInfoPath(fields[4])
		if err != nil || !filepath.IsAbs(point) {
			return false, fmt.Errorf("%w: invalid mount point %q", errMalformedMountInfo, fields[4])
		}
		if filepath.Clean(point) == target {
			mounted = true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("failed to read mount information: %w", err)
	}
	if !seen {
		return false, fmt.Errorf("%w: empty table", errMalformedMountInfo)
	}
	return mounted, nil
}

func decodeMountInfoPath(value string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("%w: truncated mount escape", errMalformedMountInfo)
		}
		v, err := strconv.ParseUint(value[i+1:i+4], 8, 8)
		if err != nil || (v != 0o40 && v != 0o11 && v != 0o12 && v != 0o134) {
			return "", fmt.Errorf("%w: invalid mount escape", errMalformedMountInfo)
		}
		decoded.WriteByte(byte(v))
		i += 3
	}
	return decoded.String(), nil
}
