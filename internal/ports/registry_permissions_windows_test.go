//go:build windows

package ports

import (
	"errors"
	"os"
	"testing"
	"time"
)

type registryPermissionTestInfo struct {
	name string
	mode os.FileMode
}

func (info registryPermissionTestInfo) Name() string       { return info.name }
func (info registryPermissionTestInfo) Size() int64        { return 0 }
func (info registryPermissionTestInfo) Mode() os.FileMode  { return info.mode }
func (info registryPermissionTestInfo) ModTime() time.Time { return time.Time{} }
func (info registryPermissionTestInfo) IsDir() bool        { return info.mode.IsDir() }
func (info registryPermissionTestInfo) Sys() any           { return nil }

func matchingRegistrySecurityInspector() registrySecurityInspector {
	return registrySecurityInspector{
		isReparsePoint:  func(string) (bool, error) { return false, nil },
		objectOwnerSID:  func(string) ([]byte, error) { return []byte{1, 2, 3}, nil },
		processOwnerSID: func() ([]byte, error) { return []byte{1, 2, 3}, nil },
	}
}

func TestVerifyRegistryOwnerMatchesCurrentTokenOwner(t *testing.T) {
	info := registryPermissionTestInfo{name: "ports.json", mode: 0o600}
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, matchingRegistrySecurityInspector()); err != nil {
		t.Fatalf("verifyRegistryOwner() error = %v", err)
	}
}

func TestVerifyRegistryOwnerRejectsMismatchedOwner(t *testing.T) {
	inspector := matchingRegistrySecurityInspector()
	inspector.processOwnerSID = func() ([]byte, error) { return []byte{9, 9, 9}, nil }
	info := registryPermissionTestInfo{name: "ports.json", mode: 0o600}
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, inspector); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("verifyRegistryOwner() error = %v, want permission error", err)
	}
}

func TestVerifyRegistryOwnerRejectsReparsePointAndInspectionErrors(t *testing.T) {
	info := registryPermissionTestInfo{name: "ports.json", mode: 0o600}
	inspector := matchingRegistrySecurityInspector()
	inspector.isReparsePoint = func(string) (bool, error) { return true, nil }
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, inspector); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("reparse-point verification error = %v, want permission error", err)
	}
	inspector.isReparsePoint = func(string) (bool, error) { return false, errors.New("attributes unavailable") }
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, inspector); err == nil {
		t.Fatal("reparse-point inspection unexpectedly succeeded")
	}
}

func TestVerifyRegistryOwnerFailsClosedWithoutPathOrForSymlink(t *testing.T) {
	inspector := matchingRegistrySecurityInspector()
	info := registryPermissionTestInfo{name: "ports.json", mode: 0o600}
	if err := verifyRegistryOwner(info, nil, inspector); err == nil {
		t.Fatal("verification without an object path unexpectedly succeeded")
	}
	symlink := registryPermissionTestInfo{name: "ports.json", mode: os.ModeSymlink | 0o600}
	if err := verifyRegistryOwner(symlink, []string{"C:\\ruk\\ports.json"}, inspector); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("symlink verification error = %v, want permission error", err)
	}
}

func TestVerifyRegistryOwnerFailsClosedOnOwnerOrProcessInspectionError(t *testing.T) {
	info := registryPermissionTestInfo{name: "ports.json", mode: 0o600}
	inspector := matchingRegistrySecurityInspector()
	inspector.objectOwnerSID = func(string) ([]byte, error) { return nil, errors.New("owner unavailable") }
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, inspector); err == nil {
		t.Fatal("owner inspection unexpectedly succeeded")
	}
	inspector = matchingRegistrySecurityInspector()
	inspector.processOwnerSID = func() ([]byte, error) { return nil, errors.New("token unavailable") }
	if err := verifyRegistryOwner(info, []string{"C:\\ruk\\ports.json"}, inspector); err == nil {
		t.Fatal("process-token-owner inspection unexpectedly succeeded")
	}
}
