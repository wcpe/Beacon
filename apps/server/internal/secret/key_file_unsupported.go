//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package secret

import (
	"fmt"
	"os"
)

func openPrivateKeyFile(path string) (*os.File, bool, error) {
	return nil, false, fmt.Errorf("当前系统不支持验证本地密钥文件最小权限")
}
