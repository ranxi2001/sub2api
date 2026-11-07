//go:build unit

package bepusdt

import (
	belog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

// InitializeForTest starts the real collection database without network workers.
// Tests use it to confirm amount allocation, configuration and order lifecycle.
func InitializeForTest(dir string) (func(), error) {
	runtime.Lock()
	defer runtime.Unlock()
	if err := initialize(dir, false); err != nil {
		return nil, err
	}
	return func() {
		runtime.Lock()
		defer runtime.Unlock()
		model.Close()
		belog.Close()
		_ = runtime.lock.Unlock()
		runtime.lock, runtime.cancel, runtime.ready = nil, nil, false
	}, nil
}
