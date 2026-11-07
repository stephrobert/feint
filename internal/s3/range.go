package s3

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	errRangeUnsatisfiable = errors.New("range does not overlap the object")
	errRangeUnsupported   = errors.New("only one bytes range is supported")
)

func byteRange(value string, size int64) (offset, length int64, err error) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, errRangeUnsupported
	}
	first, last, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok || (first == "" && last == "") {
		return 0, 0, errors.New("invalid byte range")
	}
	number := func(s string) (int64, error) {
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, errors.New("range bounds must be decimal digits")
			}
		}
		return strconv.ParseInt(s, 10, 64)
	}
	if first == "" {
		n, err := number(last)
		if err != nil {
			return 0, 0, fmt.Errorf("suffix range: %w", err)
		}
		if n == 0 || size == 0 {
			return 0, 0, errRangeUnsatisfiable
		}
		n = min(n, size)
		return size - n, n, nil
	}
	start, err := number(first)
	if err != nil {
		return 0, 0, fmt.Errorf("range start: %w", err)
	}
	end := size - 1
	if last != "" {
		end, err = number(last)
		if err != nil || end < start {
			return 0, 0, fmt.Errorf("invalid range end: %w", errors.Join(err, errors.New("end must not precede start")))
		}
	}
	if start >= size {
		return 0, 0, errRangeUnsatisfiable
	}
	end = min(end, size-1)
	return start, end - start + 1, nil
}
