package credentials

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockPublication(root *os.Root, flags int) (*os.File, error) {
	lock, err := root.OpenFile(".publish-lock", flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open credential publication lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock credential publication: %w", err)
	}
	return lock, nil
}

// durableCurrent belongs to one polling loop. Pin the actual symlink inode so
// another writer's switch cannot alias a cached digest (including ABA or inode
// reuse). A new process starts with no durability knowledge.
type durableCurrent struct {
	link *os.File
}

func (d *durableCurrent) forget() {
	if d != nil && d.link != nil {
		_ = d.link.Close()
		d.link = nil
	}
}

func (d *durableCurrent) matches(root *os.Root) bool {
	if d == nil || d.link == nil {
		return false
	}
	old, oldErr := d.link.Stat()
	now, nowErr := root.Lstat("current")
	return oldErr == nil && nowErr == nil && os.SameFile(old, now)
}

func (d *durableCurrent) remember(root *os.Root) {
	if d != nil {
		d.forget()
		// Failure to pin disables the optimization; the fsync already succeeded.
		if link, err := root.OpenFile("current", unix.O_PATH|unix.O_NOFOLLOW, 0); err == nil {
			d.link = link
		}
	}
}

func switchCurrent(root *os.Root, previous, name string, durable *durableCurrent) error {
	if previous == name && durable.matches(root) {
		return nil
	}
	durable.forget()
	if previous != name {
		// A crashed writer may leave its staging link behind.
		const next = ".current-next"
		if err := root.Remove(next); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := root.Symlink(name, next); err != nil {
			return err
		}
		if err := root.Rename(next, "current"); err != nil {
			return err
		}
	}
	if err := syncRoot(root); err != nil {
		return fmt.Errorf("current points to generation %s but its durability is unconfirmed: %w", name, err)
	}
	durable.remember(root)
	return nil
}

func syncRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func stageGeneration(root *os.Root, name, previous string, files map[string][]byte, durable *durableCurrent) error {
	from := openLinkSource(root, previous, durable)
	if from != nil {
		defer func() { _ = from.Close() }()
	}
	temp, err := os.MkdirTemp(root.Name(), ".generation-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	staging := filepath.Base(temp)
	for filename, data := range files {
		if from != nil && isCredentialPEM(filename) {
			linked, err := linkIdenticalPEM(root, from, previous, staging, filename, data)
			if err != nil {
				return err
			}
			if linked {
				continue
			}
		}
		if err := writeSynced(filepath.Join(temp, filename), data); err != nil {
			return err
		}
	}
	if err := syncDirectory(temp); err != nil {
		return err
	}
	return root.Rename(staging, name)
}

func openLinkSource(root *os.Root, previous string, durable *durableCurrent) *os.Root {
	if digest, err := hex.DecodeString(previous); err != nil || len(digest) != sha256.Size {
		return nil
	}
	from, err := openPrivateGeneration(root, previous)
	if err != nil {
		return nil
	}
	// After restart or an ambiguous switch we cannot assume the previous
	// directory entry is durable. If we cannot establish it, copy instead.
	if !durable.matches(root) && syncRoot(root) != nil {
		_ = from.Close()
		return nil
	}
	return from
}

func isCredentialPEM(name string) bool {
	return name == "tls.crt" || name == "tls.key" || name == "ca.crt"
}

func linkIdenticalPEM(root, from *os.Root, previous, staging, name string, expected []byte) (bool, error) {
	file, info, err := openPrivateFile(from, name)
	if err != nil {
		return false, nil
	}
	defer func() { _ = file.Close() }()
	if info.Size() != int64(len(expected)) {
		return false, nil
	}
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(data, expected) {
		return false, nil
	}
	linked := filepath.Join(staging, name)
	if linkErr := root.Link(filepath.Join(previous, name), linked); linkErr != nil {
		return false, nil
	}
	// Confirm that link used the same regular file we read through the pinned
	// directory, not a replacement path. Never chmod or write a linked inode.
	after, err := root.Lstat(linked)
	if err == nil && privateFile(after, 0o600) && os.SameFile(info, after) {
		return true, nil
	}
	if removeErr := root.Remove(linked); removeErr != nil {
		return false, removeErr
	}
	return false, nil
}

func verifyGeneration(root *os.Root, name string, files map[string][]byte) error {
	installed, err := openPrivateGeneration(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = installed.Close() }()
	for filename, expected := range files {
		if filename == "tls.key" {
			continue
		}
		stored, readErr := readPrivateFile(installed, filename)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(stored, expected) {
			return fmt.Errorf("stored generation is damaged: %s", filename)
		}
	}
	key, err := readPrivateFile(installed, "tls.key")
	if err == nil && !bytes.Equal(key, files["tls.key"]) {
		// The generation ID binds the leaf, not the key's PEM encoding. A
		// different encoding is equivalent only if it still matches that leaf.
		_, err = tls.X509KeyPair(files["tls.crt"], key)
	}
	if err != nil {
		return fmt.Errorf("stored private key is unusable; reissue the leaf to publish a new generation: %w", err)
	}
	return nil
}

func openPrivateGeneration(root *os.Root, name string) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !privateFile(info, os.ModeDir|0o700) {
		return nil, fmt.Errorf("generation is not an owner-only directory: %s", name)
	}
	generation, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := generation.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = generation.Close()
		return nil, fmt.Errorf("generation changed while opening: %s", name)
	}
	return generation, nil
}

func privateFile(info os.FileInfo, mode os.FileMode) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode() == mode && int64(stat.Uid) == int64(os.Geteuid())
}

func openPrivateFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err == nil && privateFile(info, 0o600) {
		return file, info, nil
	}
	_ = file.Close()
	return nil, nil, fmt.Errorf("generation file is not an owner-only regular file: %s", name)
}

func readPrivateFile(root *os.Root, name string) ([]byte, error) {
	file, _, err := openPrivateFile(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}
