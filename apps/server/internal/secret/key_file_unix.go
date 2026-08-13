//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openPrivateKeyFile(path string) (*os.File, bool, error) {
	directory, name, err := privateKeyPathParts(path)
	if err != nil {
		return nil, false, err
	}
	dir, err := openPrivateKeyDirectory(directory)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	file, err := openExistingPrivateKeyFile(dir, name)
	if err == nil {
		return file, false, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return nil, false, fmt.Errorf("读取加密密钥文件失败 %s: %w", path, err)
	}
	file, err = createPrivateKeyFile(dir, name)
	if err == nil {
		return file, true, nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return nil, false, fmt.Errorf("创建加密密钥文件失败 %s: %w", path, err)
	}
	file, err = openExistingPrivateKeyFile(dir, name)
	if err != nil {
		return nil, false, fmt.Errorf("读取并发创建的加密密钥文件失败 %s: %w", path, err)
	}
	return file, false, nil
}

func privateKeyPathParts(path string) (string, string, error) {
	directory, name := filepath.Dir(path), filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		return "", "", fmt.Errorf("加密密钥文件路径无效 %s", path)
	}
	return directory, name, nil
}

func openPrivateKeyDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		if createErr := unix.Mkdir(path, 0o700); createErr != nil && !errors.Is(createErr, unix.EEXIST) {
			return nil, fmt.Errorf("创建密钥目录失败 %s: %w", path, createErr)
		}
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("打开密钥目录失败 %s: %w", path, err)
	}
	dir := os.NewFile(uintptr(fd), path)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("打开密钥目录失败 %s", path)
	}
	if err := validatePrivateUnixHandle(fd, true); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("密钥目录权限不安全 %s: %w", path, err)
	}
	return dir, nil
}

func openExistingPrivateKeyFile(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateUnixHandle(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("打开密钥文件失败")
	}
	return file, nil
}

func createPrivateKeyFile(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateUnixHandle(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("创建密钥文件失败")
	}
	return file, nil
}

func validatePrivateUnixHandle(fd int, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	mode := uint64(stat.Mode)
	wantType := uint64(unix.S_IFREG)
	if directory {
		wantType = uint64(unix.S_IFDIR)
	}
	if mode&uint64(unix.S_IFMT) != wantType {
		return fmt.Errorf("不是受支持的文件类型")
	}
	uid := os.Geteuid()
	if uid < 0 || stat.Uid != uint32(uid) {
		return fmt.Errorf("所有者不是当前进程用户")
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("包含组或其他用户权限")
	}
	return nil
}
