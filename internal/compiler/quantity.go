package compiler

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

var quantityPattern = regexp.MustCompile("^([0-9]+(?:\\.[0-9]+)?)(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)$")

var byteFactors = map[string]int64{
	"B":   1,
	"KB":  1_000,
	"MB":  1_000_000,
	"GB":  1_000_000_000,
	"TB":  1_000_000_000_000,
	"KiB": 1 << 10,
	"MiB": 1 << 20,
	"GiB": 1 << 30,
	"TiB": 1 << 40,
}

func parseBytes(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	match := quantityPattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, fmt.Errorf("must use a Skali byte unit such as MB, GB, MiB, or GiB")
	}
	number, ok := new(big.Rat).SetString(match[1])
	if !ok {
		return 0, fmt.Errorf("invalid numeric quantity %q", match[1])
	}
	number.Mul(number, big.NewRat(byteFactors[match[2]], 1))
	if !number.IsInt() {
		return 0, fmt.Errorf("%q does not resolve to a whole number of bytes", value)
	}
	if !number.Num().IsInt64() {
		return 0, fmt.Errorf("%q exceeds the supported byte range", value)
	}
	result := number.Num().Int64()
	if result <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return result, nil
}

func parseCPU(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	number, ok := new(big.Rat).SetString(value)
	if !ok || number.Sign() <= 0 {
		return 0, fmt.Errorf("must be a positive number of CPU cores")
	}
	number.Mul(number, big.NewRat(1000, 1))
	if !number.IsInt() {
		return 0, fmt.Errorf("supports at most three decimal places")
	}
	if !number.Num().IsInt64() {
		return 0, fmt.Errorf("exceeds the supported CPU range")
	}
	return number.Num().Int64(), nil
}

func parseDuration(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	multiplier := time.Duration(1)
	number := value
	switch {
	case strings.HasSuffix(value, "w"):
		multiplier = 7 * 24 * time.Hour
		number = strings.TrimSuffix(value, "w")
	case strings.HasSuffix(value, "d"):
		multiplier = 24 * time.Hour
		number = strings.TrimSuffix(value, "d")
	default:
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return 0, fmt.Errorf("must be a positive duration such as 30s, 5m, or 7d")
		}
		return duration.Milliseconds(), nil
	}
	valueNumber, ok := new(big.Rat).SetString(number)
	if !ok || valueNumber.Sign() <= 0 {
		return 0, fmt.Errorf("must be a positive duration such as 30s, 5m, or 7d")
	}
	milliseconds := new(big.Rat).Mul(valueNumber, big.NewRat(int64(multiplier), int64(time.Millisecond)))
	if !milliseconds.IsInt() || !milliseconds.Num().IsInt64() {
		return 0, fmt.Errorf("duration is outside the supported millisecond range")
	}
	return milliseconds.Num().Int64(), nil
}
