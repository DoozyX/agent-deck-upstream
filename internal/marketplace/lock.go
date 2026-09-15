package marketplace

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

const lockWait = 5 * time.Second

// Each acquisition opens its own file description, so flock serializes both
// processes and goroutines on supported Unix hosts. Never unlink this inode.
type hostLock struct{ file *os.File }
type gitLockKey struct{}

func (lock *hostLock) release() {
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	_ = lock.file.Close()
}

func acquireLock(ctx context.Context, path string) (*hostLock, error) {
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("marketplace: lock unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("marketplace: invalid lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &hostLock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, errors.New("marketplace: lock failed")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
