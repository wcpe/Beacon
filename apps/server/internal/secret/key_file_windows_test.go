//go:build windows

package secret

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLoadOrCreateCipherRejectsInsecureWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if _, err := LoadOrCreateCipher(path); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	grantEveryoneWindowsACL(t, path)
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("向 Everyone 授权的密钥文件应拒绝启动")
	}
}

func TestLoadOrCreateCipherRejectsInsecureWindowsDirectoryACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if _, err := LoadOrCreateCipher(path); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	grantEveryoneWindowsACL(t, filepath.Dir(path))
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("向 Everyone 授权的密钥目录应拒绝启动")
	}
}

func TestPrivateKeyDirectoryHandlePreventsReplacementDuringFileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	if _, err := LoadOrCreateCipher(path); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	directory := filepath.Dir(path)
	sid, err := currentPrivateKeySID()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := openPrivateKeyDirectory(directory, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("关闭密钥目录句柄失败: %v", err)
		}
	}()
	if err := os.Rename(directory, filepath.Join(t.TempDir(), "replaced-secrets")); err == nil {
		t.Fatal("持有密钥目录句柄期间不应允许替换该目录")
	}
	file, err := openExistingPrivateKeyFileAt(handle, filepath.Base(path), sid)
	if err != nil {
		t.Fatalf("应通过已验证目录句柄打开密钥文件: %v", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil || len(raw) == 0 {
		t.Fatalf("应从已验证目录句柄读取密钥文件: len=%d err=%v", len(raw), err)
	}
}

func TestLoadOrCreateCipherRejectsJunctionedKeyDirectory(t *testing.T) {
	targetDirectory := filepath.Join(t.TempDir(), "target-secrets")
	if err := os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets", "config-encryption.key")
	createWindowsJunction(t, filepath.Dir(path), targetDirectory)
	if _, err := LoadOrCreateCipher(path); err == nil {
		t.Fatal("junction 密钥目录应拒绝启动")
	}
}

func createWindowsJunction(t *testing.T, link, target string) {
	t.Helper()
	command := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("创建 junction 失败: %v output=%s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("cmd", "/c", "rmdir", link).CombinedOutput(); err != nil {
			t.Errorf("删除 junction 失败: %v output=%s", err, output)
		}
	})
}

func grantEveryoneWindowsACL(t *testing.T, path string) {
	t.Helper()
	everyone, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Skipf("当前文件系统不支持 Windows ACL: %v", err)
	}
}
