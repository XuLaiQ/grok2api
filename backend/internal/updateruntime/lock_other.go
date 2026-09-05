//go:build !linux

package updateruntime

import "errors"

func lockSupervisor(string) (func(), error) {
	return nil, errors.New("managed updates require Linux")
}
