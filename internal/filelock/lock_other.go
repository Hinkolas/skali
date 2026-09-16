//go:build !unix

package filelock

import (
	"errors"
	"os"
)

func tryLock(*os.File, bool) (bool, error) {
	return false, errors.New("skali state locking requires Linux or macOS")
}
