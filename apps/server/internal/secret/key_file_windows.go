//go:build windows

package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openPrivateKeyFile(path string) (*os.File, bool, error) {
	directory, name, err := privateKeyPathParts(path)
	if err != nil {
		return nil, false, err
	}
	sid, err := currentPrivateKeySID()
	if err != nil {
		return nil, false, fmt.Errorf("读取密钥文件所有者失败: %w", err)
	}
	dir, err := openPrivateKeyDirectory(directory, sid)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		_ = windows.CloseHandle(dir)
	}()
	file, err := openExistingPrivateKeyFileAt(dir, name, sid)
	if err == nil {
		return file, false, nil
	}
	if !isWindowsNotFound(err) {
		return nil, false, fmt.Errorf("读取加密密钥文件失败 %s: %w", path, err)
	}
	file, err = createPrivateKeyFileAt(dir, name, sid)
	if err == nil {
		return file, true, nil
	}
	if !isWindowsAlreadyExists(err) {
		return nil, false, fmt.Errorf("创建加密密钥文件失败 %s: %w", path, err)
	}
	file, err = openExistingPrivateKeyFileAt(dir, name, sid)
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

func currentPrivateKeySID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func openPrivateKeyDirectory(path string, sid *windows.SID) (windows.Handle, error) {
	handle, err := openPrivateKeyDirectoryHandle(path)
	if err == nil {
		if err := validatePrivateWindowsHandle(handle, true, sid); err != nil {
			_ = windows.CloseHandle(handle)
			return windows.InvalidHandle, fmt.Errorf("密钥目录权限不安全 %s: %w", path, err)
		}
		return handle, nil
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return windows.InvalidHandle, fmt.Errorf("打开密钥目录失败 %s: %w", path, err)
	}
	attributes, err := privateSecurityAttributes(sid)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(path), attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return windows.InvalidHandle, fmt.Errorf("创建密钥目录失败 %s: %w", path, err)
	}
	handle, err = openPrivateKeyDirectoryHandle(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("打开密钥目录失败 %s: %w", path, err)
	}
	if err := validatePrivateWindowsHandle(handle, true, sid); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, fmt.Errorf("密钥目录权限不安全 %s: %w", path, err)
	}
	return handle, nil
}

func openPrivateKeyDirectoryHandle(path string) (windows.Handle, error) {
	return windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func openExistingPrivateKeyFileAt(directory windows.Handle, name string, sid *windows.SID) (*os.File, error) {
	return openPrivateKeyFileAt(directory, name, sid, windows.FILE_OPEN)
}

func createPrivateKeyFileAt(directory windows.Handle, name string, sid *windows.SID) (*os.File, error) {
	return openPrivateKeyFileAt(directory, name, sid, windows.FILE_CREATE)
}

func openPrivateKeyFileAt(directory windows.Handle, name string, sid *windows.SID, disposition uint32) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := uint32(windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE)
	object := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: directory,
		ObjectName:    objectName,
		Attributes:    attributes,
	}
	access := uint32(windows.FILE_GENERIC_READ)
	if disposition == windows.FILE_CREATE {
		descriptor, err := privateSecurityDescriptor(sid)
		if err != nil {
			return nil, err
		}
		object.SecurityDescriptor = descriptor
		access |= windows.FILE_GENERIC_WRITE
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access, object, &status, nil, 0, 0, disposition,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateWindowsHandle(handle, false, sid); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("打开密钥文件失败")
	}
	return file, nil
}

func privateSecurityAttributes(sid *windows.SID) (*windows.SecurityAttributes, error) {
	descriptor, err := privateSecurityDescriptor(sid)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

func isWindowsNotFound(err error) bool {
	status, ok := err.(windows.NTStatus)
	return ok && status == windows.STATUS_OBJECT_NAME_NOT_FOUND
}

func isWindowsAlreadyExists(err error) bool {
	status, ok := err.(windows.NTStatus)
	return ok && status == windows.STATUS_OBJECT_NAME_COLLISION
}

func privateSecurityDescriptor(sid *windows.SID) (*windows.SECURITY_DESCRIPTOR, error) {
	owner := sid.String()
	if owner == "" {
		return nil, fmt.Errorf("读取当前进程用户标识失败")
	}
	return windows.SecurityDescriptorFromString("O:" + owner + "D:P(A;;FA;;;" + owner + ")")
}

func validatePrivateWindowsHandle(handle windows.Handle, directory bool, sid *windows.SID) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("不允许重解析点")
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory {
		return fmt.Errorf("不是受支持的文件类型")
	}
	if !directory {
		fileType, err := windows.GetFileType(handle)
		if err != nil || fileType != windows.FILE_TYPE_DISK {
			return fmt.Errorf("不是普通磁盘文件")
		}
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("缺少安全描述符")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, sid) {
		return fmt.Errorf("所有者不是当前进程用户")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("访问控制列表未受保护")
	}
	expected, err := privateSecurityDescriptor(sid)
	if err != nil {
		return err
	}
	if descriptor.String() != expected.String() {
		return fmt.Errorf("访问控制列表不是仅当前进程用户")
	}
	return nil
}
