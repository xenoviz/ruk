//go:build windows

package ports

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	registryFileAttributeReparsePoint = 0x00000400
	registrySecurityObjectFile        = 1 // SE_FILE_OBJECT
	registryOwnerSecurityInformation  = 0x00000001
)

var registryAdvapi32 = syscall.NewLazyDLL("advapi32.dll")

var registryGetNamedSecurityInfo = registryAdvapi32.NewProc("GetNamedSecurityInfoW")

// registrySecurityInspector is deliberately small so ownership policy can be
// tested without loading Windows APIs. The production implementation below
// uses only APIs from the Go standard library and Win32 system DLLs.
type registrySecurityInspector struct {
	isReparsePoint func(string) (bool, error)
	objectOwnerSID func(string) ([]byte, error)
	processUserSID func() ([]byte, error)
}

var nativeRegistrySecurityInspector = registrySecurityInspector{
	isReparsePoint: registryIsReparsePoint,
	objectOwnerSID: registryObjectOwnerSID,
	processUserSID: registryProcessUserSID,
}

func verifyRegistryRootOwner(info os.FileInfo, path string) error {
	return verifyRegistryOwner(info, []string{path}, nativeRegistrySecurityInspector)
}

func verifyRegistryFileOwner(info os.FileInfo, path string) error {
	return verifyRegistryOwner(info, []string{path}, nativeRegistrySecurityInspector)
}

func verifyRegistryOwner(info os.FileInfo, paths []string, inspector registrySecurityInspector) error {
	if info == nil {
		return fmt.Errorf("file information is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.ErrPermission
	}
	if len(paths) != 1 || paths[0] == "" || !filepath.IsAbs(paths[0]) {
		return fmt.Errorf("object path is unavailable")
	}
	if inspector.isReparsePoint == nil || inspector.objectOwnerSID == nil || inspector.processUserSID == nil {
		return fmt.Errorf("native ownership inspection is unavailable")
	}
	reparsePoint, err := inspector.isReparsePoint(paths[0])
	if err != nil {
		return fmt.Errorf("inspect reparse-point state: %w", err)
	}
	if reparsePoint {
		return os.ErrPermission
	}
	objectSID, err := inspector.objectOwnerSID(paths[0])
	if err != nil {
		return fmt.Errorf("inspect object owner: %w", err)
	}
	processSID, err := inspector.processUserSID()
	if err != nil {
		return fmt.Errorf("inspect process user: %w", err)
	}
	if len(objectSID) == 0 || len(processSID) == 0 || !bytes.Equal(objectSID, processSID) {
		return os.ErrPermission
	}
	return nil
}

func registryIsReparsePoint(path string) (bool, error) {
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("encode object path: %w", err)
	}
	attributes, err := syscall.GetFileAttributes(pathUTF16)
	if err != nil {
		return false, err
	}
	return attributes&registryFileAttributeReparsePoint != 0, nil
}

// registryObjectOwnerSID obtains the owner SID with GetNamedSecurityInfoW.
// That API allocates the returned security descriptor with LocalAlloc; the
// descriptor is copied before LocalFree so no pointer escapes its lifetime.
// This validates ownership only; it does not evaluate the effective DACL, so
// an owner SID match alone does not prove that other principals cannot write.
func registryObjectOwnerSID(path string) (sid []byte, resultErr error) {
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode object path: %w", err)
	}
	var ownerSID *syscall.SID
	var groupSID *syscall.SID
	var dacl *byte
	var sacl *byte
	var descriptor *byte
	result, _, _ := registryGetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(pathUTF16)),
		registrySecurityObjectFile,
		registryOwnerSecurityInformation,
		uintptr(unsafe.Pointer(&ownerSID)),
		uintptr(unsafe.Pointer(&groupSID)),
		uintptr(unsafe.Pointer(&dacl)),
		uintptr(unsafe.Pointer(&sacl)),
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if descriptor != nil {
		defer func() {
			if _, freeErr := syscall.LocalFree(syscall.Handle(unsafe.Pointer(descriptor))); freeErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("free security descriptor: %w", freeErr)
				sid = nil
			}
		}()
	}
	if result != 0 {
		return nil, syscall.Errno(result)
	}
	if ownerSID == nil || descriptor == nil {
		return nil, fmt.Errorf("security descriptor did not contain an owner SID")
	}
	length := syscall.GetLengthSid(ownerSID)
	if length == 0 {
		return nil, fmt.Errorf("security descriptor contained an invalid owner SID")
	}
	ownerBytes := unsafe.Slice((*byte)(unsafe.Pointer(ownerSID)), length)
	sid = append([]byte(nil), ownerBytes...)
	return sid, nil
}

// registryProcessUserSID obtains TokenUser for the current process. The
// access token is a native handle and is closed on every path after opening.
func registryProcessUserSID() (sid []byte, resultErr error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := token.Close(); closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("close process token: %w", closeErr)
			sid = nil
		}
	}()

	var required uint32
	err = syscall.GetTokenInformation(token, syscall.TokenUser, nil, 0, &required)
	if err != syscall.ERROR_INSUFFICIENT_BUFFER || required == 0 {
		if err == nil {
			return nil, fmt.Errorf("token user size query unexpectedly succeeded")
		}
		return nil, err
	}
	buffer := make([]byte, required)
	if err := syscall.GetTokenInformation(token, syscall.TokenUser, &buffer[0], uint32(len(buffer)), &required); err != nil {
		return nil, err
	}
	if required < uint32(unsafe.Sizeof(syscall.Tokenuser{})) {
		return nil, fmt.Errorf("token user information is truncated")
	}
	tokenUser := (*syscall.Tokenuser)(unsafe.Pointer(&buffer[0]))
	if tokenUser.User.Sid == nil {
		return nil, fmt.Errorf("token user information did not contain a SID")
	}
	length := syscall.GetLengthSid(tokenUser.User.Sid)
	if length == 0 {
		return nil, fmt.Errorf("token user information contained an invalid SID")
	}
	userBytes := unsafe.Slice((*byte)(unsafe.Pointer(tokenUser.User.Sid)), length)
	sid = append([]byte(nil), userBytes...)
	return sid, nil
}
