package marketplace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestManagedCheckoutProcessHelper(t *testing.T) {
	home := os.Getenv("MARKETPLACE_TEST_HOME")
	if home == "" {
		return
	}
	trustedGitPath = filepath.Join(home, "bin", "git")
	id := os.Getenv("MARKETPLACE_TEST_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := WithManagedCheckout(ctx, home, func(c Checkout) error {
		if err := os.WriteFile(filepath.Join(home, id+".ready"), []byte(c.Revision), 0600); err != nil {
			return err
		}
		for os.Getenv("MARKETPLACE_TEST_HOLD") == "1" {
			if _, err := os.Stat(filepath.Join(home, "release")); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func startCheckout(t *testing.T, f fixture, id string, hold bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedCheckoutProcessHelper$")
	cmd.Env = append(os.Environ(), "MARKETPLACE_TEST_HOME="+f.home, "MARKETPLACE_TEST_ID="+id)
	if hold {
		cmd.Env = append(cmd.Env, "MARKETPLACE_TEST_HOLD=1")
	}
	output, err := os.Create(filepath.Join(f.home, id+".output"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = output.Close()
	})
	return cmd
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("barrier not reached: %s", path)
}
func TestManagedCheckoutProcessLock(t *testing.T) {
	f := newFixture(t)
	first := startCheckout(t, f, "first", true)
	waitFile(t, filepath.Join(f.home, "first.ready"))
	second := startCheckout(t, f, "second", false)
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(filepath.Join(f.home, "second.ready")); err == nil {
		t.Fatal("second callback entered while first held host lock")
	}
	writeTest(t, filepath.Join(f.home, "release"), "release")
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(f.home, "second.ready"))
	data, err := os.ReadFile(filepath.Join(f.home, "fetches"))
	if err != nil || string(data) != "fetch\n" {
		t.Fatalf("contending processes fetched %q: %v", data, err)
	}
}
func TestManagedCheckoutCrashReleasesLock(t *testing.T) {
	f := newFixture(t)
	first := startCheckout(t, f, "first", true)
	waitFile(t, filepath.Join(f.home, "first.ready"))
	lock := filepath.Join(f.home, ".local/state/agent-deck/marketplace-update/update.lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	err = WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("callback after interrupted check"); return nil })
	if !errors.Is(err, ErrBackoff) {
		t.Fatalf("crash retry=%v", err)
	}
	state, err := loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	state.NextAttempt = time.Now().Add(-time.Second)
	for key, retry := range state.CallbackRetries {
		retry.NextAttempt = time.Now().Add(-time.Second)
		state.CallbackRetries[key] = retry
	}
	if err = saveState(StateDir(f.home), state); err != nil {
		t.Fatal(err)
	}
	second := startCheckout(t, f, "second", false)
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock inode replaced after process crash")
	}
}
func TestManagedCheckoutLockDeadline(t *testing.T) {
	f := newFixture(t)
	first := startCheckout(t, f, "first", true)
	_ = first
	waitFile(t, filepath.Join(f.home, "first.ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := WithManagedCheckout(ctx, f.home, func(Checkout) error { t.Fatal("callback under contended lock"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", err)
	}
}

func TestManagedCheckoutCrashDuringFetchKeepsLock(t *testing.T) {
	f := newFixture(t)
	writeTest(t, filepath.Join(f.home, "git-mode"), "barrier")
	first := startCheckout(t, f, "first", false)
	waitFile(t, filepath.Join(f.home, "git-mode.pid"))
	data, _ := os.ReadFile(filepath.Join(f.home, "git-mode.pid"))
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	fd, err := syscall.Open(filepath.Join(StateDir(f.home), "update.lock"), syscall.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("orphaned Git released lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = WithManagedCheckout(ctx, f.home, func(Checkout) error { t.Fatal("callback while orphaned Git holds lock"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("orphaned Git did not retain host lock: %v", err)
	}
	writeTest(t, filepath.Join(f.home, "git-release"), "release")
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) && syscall.Kill(pid, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("released Git process remains alive")
	}
	err = WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("callback during crash backoff"); return nil })
	if !errors.Is(err, ErrBackoff) {
		t.Fatalf("post-crash retry=%v", err)
	}
}
