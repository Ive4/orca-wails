package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestZIP64DirectoryWithSmallCoreArchive(t *testing.T) {
	path, _ := fixture(t, "v1.4.266", "", macTarget)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset := len(data) - 22
	end := bytes.Clone(data[offset:])
	u32 := binary.LittleEndian
	directoryOffset, directorySize := u32.Uint32(end[16:]), u32.Uint32(end[12:])
	var record [56]byte
	u32.PutUint32(record[:], 0x06064b50)
	u32.PutUint64(record[4:], 44)
	u32.PutUint64(record[24:], 2)
	u32.PutUint64(record[32:], 2)
	u32.PutUint64(record[40:], uint64(directorySize))
	u32.PutUint64(record[48:], uint64(directoryOffset))
	var locator [20]byte
	u32.PutUint32(locator[:], 0x07064b50)
	u32.PutUint64(locator[8:], uint64(offset))
	u32.PutUint32(locator[16:], 1)
	u32.PutUint32(end[8:], 0xffffffff)
	u32.PutUint32(end[12:], 0xffffffff)
	u32.PutUint32(end[16:], 0xffffffff)
	zip64 := append(data[:offset], record[:]...)
	zip64 = append(zip64, locator[:]...)
	zip64 = append(zip64, end...)
	if err := os.WriteFile(path, zip64, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchive(path); err != nil {
		t.Fatalf("rejected ZIP64 core: %v", err)
	}
	u32.PutUint64(zip64[offset+24:], 80_001)
	u32.PutUint64(zip64[offset+32:], 80_001)
	if err := os.WriteFile(path, zip64, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchive(path); err == nil {
		t.Fatal("accepted oversized ZIP64 entry count")
	}
}

func TestCorePreparationRejectsCorruptPayload(t *testing.T) {
	path, _ := fixture(t, "v1.4.266", "", macTarget, zipEntry{"file", "target for corruption", 0o600})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Change only the stored CRC for the last file in the central directory.
	signature := []byte{'P', 'K', 1, 2}
	offset := bytes.LastIndex(data, signature)
	data[offset+16] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Store{Root: t.TempDir(), Target: macTarget}
	if _, err := s.PrepareArchive(path, fmt.Sprintf("%x", sha256.Sum256(data))); err == nil {
		t.Fatal("accepted archive whose payload CRC is invalid")
	}
	slots, err := os.ReadDir(filepath.Join(s.Root, "versions"))
	if err != nil || len(slots) != 0 {
		t.Fatalf("failed extraction left slots %v, %v", slots, err)
	}
}
