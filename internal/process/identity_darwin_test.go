//go:build darwin

package process

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestDarwinKinfoProcMIBUsesNumericPID(t *testing.T) {
	got, err := darwinKinfoProcMIB(1234)
	if err != nil {
		t.Fatalf("darwinKinfoProcMIB returned an error: %v", err)
	}
	want := []int32{darwinCTLKern, darwinKernProc, darwinKernProcPID, 1234}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MIB = %v, want %v", got, want)
	}
}

func TestDarwinKinfoProcMIBRejectsInvalidPID(t *testing.T) {
	if mib, err := darwinKinfoProcMIB(0); err == nil || mib != nil {
		t.Fatalf("darwinKinfoProcMIB(0) = %v, %v; want error", mib, err)
	}
}

func TestParseDarwinStartTimeUsesMicroseconds(t *testing.T) {
	first := make([]byte, darwinTimevalSize)
	binary.LittleEndian.PutUint64(first[:8], 1_786_740_000)
	binary.LittleEndian.PutUint32(first[8:12], 100)
	second := append([]byte(nil), first...)
	binary.LittleEndian.PutUint32(second[8:12], 101)

	firstIdentity, err := parseDarwinStartTime(first)
	if err != nil {
		t.Fatalf("parseDarwinStartTime(first) returned an error: %v", err)
	}
	secondIdentity, err := parseDarwinStartTime(second)
	if err != nil {
		t.Fatalf("parseDarwinStartTime(second) returned an error: %v", err)
	}
	if firstIdentity != "darwin:1786740000:100" || secondIdentity != "darwin:1786740000:101" {
		t.Fatalf("identities = %q, %q; want distinct microsecond tokens", firstIdentity, secondIdentity)
	}
}

func TestParseDarwinStartTimeRejectsMalformedRecords(t *testing.T) {
	for name, record := range map[string][]byte{
		"short":        make([]byte, darwinTimevalSize-1),
		"zero seconds": make([]byte, darwinTimevalSize),
	} {
		t.Run(name, func(t *testing.T) {
			if identity, err := parseDarwinStartTime(record); err == nil || identity != "" {
				t.Fatalf("parseDarwinStartTime = %q, %v; want error", identity, err)
			}
		})
	}

	invalidMicroseconds := make([]byte, darwinTimevalSize)
	binary.LittleEndian.PutUint64(invalidMicroseconds[:8], 1)
	binary.LittleEndian.PutUint32(invalidMicroseconds[8:12], 1_000_000)
	if identity, err := parseDarwinStartTime(invalidMicroseconds); err == nil || identity != "" {
		t.Fatalf("parseDarwinStartTime(invalid microseconds) = %q, %v; want error", identity, err)
	}
}
