package update

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	manifestName      = "core-manifest.json"
	receiptName       = ".orca-tauri-install.json"
	maxArchiveBytes   = 1 << 30
	maxFileBytes      = 512 << 20
	maxUnpackedBytes  = 2 << 30
	maxArchiveEntries = 80_000
	maxPackagePaths   = 2 * maxArchiveEntries
	maxZIPDirectory   = 64 << 20
	maxManifestBytes  = 64 << 10
)

// InspectArchive reads the manifest and validates ZIP structure and package paths.
// PrepareArchive additionally verifies the trusted SHA-256 and extracts all files.
func InspectArchive(path string) (Manifest, error) {
	file, size, err := openArchiveFile(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	archive, err := readArchive(context.Background(), file, size)
	return archive.manifest, err
}

func openArchiveFile(path string) (*os.File, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 22 || info.Size() > maxArchiveBytes {
		return nil, 0, fmt.Errorf("invalid or oversized ZIP file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err = file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 22 || info.Size() > maxArchiveBytes {
		file.Close()
		return nil, 0, fmt.Errorf("invalid or oversized ZIP file")
	}
	return file, info.Size(), nil
}

type archivePlan struct {
	manifest Manifest
	dirs     []string
	files    []*zip.File
}

type archivePath struct {
	name     string
	dir      bool
	explicit bool
}

func readArchive(ctx context.Context, file io.ReaderAt, size int64) (archivePlan, error) {
	var plan archivePlan
	count, err := checkZIPDirectory(file, size)
	if err != nil {
		return plan, err
	}
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return plan, fmt.Errorf("read core ZIP directory: %w", err)
	}
	if len(reader.File) != count {
		return plan, fmt.Errorf("ZIP entry count mismatch")
	}
	paths := make(map[string]archivePath, count)
	var manifestFile *zip.File
	var expanded uint64
	for _, entry := range reader.File {
		dir := strings.HasSuffix(entry.Name, "/")
		name := strings.TrimSuffix(entry.Name, "/")
		if err := validateRelativePath(name); err != nil {
			return plan, err
		}
		if entry.Flags&1 != 0 {
			return plan, fmt.Errorf("encrypted ZIP entries are unsupported")
		}
		if !regularZIPEntry(entry, dir) {
			return plan, fmt.Errorf("ZIP entry %q has an unsupported file type", entry.Name)
		}
		if (!dir && entry.Method != zip.Store && entry.Method != zip.Deflate) ||
			(dir && entry.UncompressedSize64 != 0) {
			return plan, fmt.Errorf("ZIP entry %q has unsupported compression or directory data", entry.Name)
		}
		length := entry.UncompressedSize64
		if length > maxFileBytes || (length > 16<<20 && length/max(1, entry.CompressedSize64) > 2000) {
			return plan, fmt.Errorf("ZIP entry %q exceeds size or compression ratio limit", entry.Name)
		}
		expanded += length
		if expanded > maxUnpackedBytes {
			return plan, fmt.Errorf("ZIP exceeds total expanded size limit")
		}

		// Track implicit parent directories too, so A/x and a/y cannot alias.
		folded := strings.ToLower(name)
		foldedRest := folded
		end, foldedEnd := 0, 0
		for component := range strings.SplitSeq(name, "/") {
			foldedComponent, rest, more := strings.Cut(foldedRest, "/")
			end += len(component)
			foldedEnd += len(foldedComponent)
			part, key, leaf := name[:end], folded[:foldedEnd], !more
			if key == receiptName {
				return plan, fmt.Errorf("archive contains reserved install receipt")
			}
			isDir := !leaf || dir
			existing, ok := paths[key]
			if ok {
				if existing.name != part || existing.dir != isDir || (leaf && existing.explicit) {
					return plan, fmt.Errorf("ZIP contains duplicate or colliding path %q", part)
				}
			} else {
				if len(paths) >= maxPackagePaths {
					return plan, fmt.Errorf("ZIP exceeds total package path limit")
				}
				if isDir {
					plan.dirs = append(plan.dirs, part)
				}
			}
			if leaf || !ok {
				paths[key] = archivePath{name: part, dir: isDir, explicit: leaf}
			}
			foldedRest = rest
			end++
			foldedEnd++
		}
		if !dir {
			plan.files = append(plan.files, entry)
			if name == manifestName {
				manifestFile = entry
			}
		}
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > maxManifestBytes {
		return plan, fmt.Errorf("missing or oversized %s at ZIP root", manifestName)
	}
	var data strings.Builder
	if err := copyZIPEntry(ctx, &data, manifestFile); err != nil {
		return plan, fmt.Errorf("read %s: %w", manifestName, err)
	}
	manifest, err := decodeManifest([]byte(data.String()))
	if err != nil {
		return plan, err
	}
	executable := paths[strings.ToLower(manifest.Executable)]
	if executable.name != manifest.Executable || executable.dir {
		return plan, fmt.Errorf("core executable is not a regular package file")
	}
	if manifest.WorkingDirectory != "." {
		workingDir := paths[strings.ToLower(manifest.WorkingDirectory)]
		if workingDir.name != manifest.WorkingDirectory || !workingDir.dir {
			return plan, fmt.Errorf("core working_directory is not a package directory")
		}
	}
	plan.manifest = manifest
	return plan, nil
}

func regularZIPEntry(entry *zip.File, dir bool) bool {
	switch entry.CreatorVersion >> 8 {
	case 3, 19: // Unix and macOS
		switch entry.ExternalAttrs >> 16 & 0xf000 {
		case 0, 0x8000: // no file type or regular file
			return true
		case 0x4000:
			return dir
		default:
			return false
		}
	case 0, 10, 14: // FAT, NTFS and VFAT
		if entry.ExternalAttrs&0x400 != 0 { // FILE_ATTRIBUTE_REPARSE_POINT
			return false
		}
	}
	return entry.Mode().IsRegular() || (dir && entry.Mode().Type() == fs.ModeDir)
}

func (plan archivePlan) extract(ctx context.Context, destination string) error {
	for _, dir := range plan.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		// No MkdirAll here. The filesystem may find an alias our map cannot see.
		if err := os.Mkdir(filepath.Join(destination, filepath.FromSlash(dir)), 0o700); err != nil {
			return err
		}
	}
	for _, entry := range plan.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if entry.Mode()&0o111 != 0 {
			mode = 0o700
		}
		path := filepath.Join(destination, filepath.FromSlash(entry.Name))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		err = copyZIPEntry(ctx, file, entry)
		if err == nil {
			err = file.Chmod(mode)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("extract ZIP entry %q: %w", entry.Name, err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for i := len(plan.dirs) - 1; i >= 0; i-- {
		if err := syncDirectory(filepath.Join(destination, filepath.FromSlash(plan.dirs[i]))); err != nil {
			return err
		}
	}
	return syncDirectory(destination)
}

func copyZIPEntry(ctx context.Context, output io.Writer, entry *zip.File) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	size := int64(entry.UncompressedSize64)
	crc := crc32.NewIEEE()
	written, err := io.Copy(io.MultiWriter(output, crc), io.LimitReader(contextReader{ctx, input}, size+1))
	if err != nil {
		return err
	}
	if written != size || crc.Sum32() != entry.CRC32 {
		return fmt.Errorf("ZIP data differs from declared length or CRC")
	}
	return nil
}

func validateRelativePath(name string) error {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) {
		return fmt.Errorf("invalid package path %q", name)
	}
	for component := range strings.SplitSeq(name, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") ||
			len(utf16.Encode([]rune(component))) > 255 ||
			strings.ContainsAny(component, `\:<>"|?*`) || strings.ContainsFunc(component, unicode.IsControl) {
			return fmt.Errorf("unsafe package path %q", name)
		}
		stem, _, _ := strings.Cut(component, ".")
		stem = strings.ToUpper(strings.TrimRight(stem, " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
			return fmt.Errorf("package path uses Windows device name %q", name)
		}
		if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
			switch stem[3:] {
			case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
				return fmt.Errorf("package path uses Windows device name %q", name)
			}
		}
	}
	return nil
}

// Check the ZIP and ZIP64 directory layout before archive/zip allocates entries.
func checkZIPDirectory(file io.ReaderAt, size int64) (int, error) {
	if size < 22 || size > maxArchiveBytes {
		return 0, fmt.Errorf("invalid or oversized ZIP file")
	}
	tail := make([]byte, min(size, 65_557))
	if _, err := file.ReadAt(tail, size-int64(len(tail))); err != nil {
		return 0, err
	}
	u16, u32, u64 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	end := len(tail) - 22
	for ; end >= 0; end-- {
		if u32(tail[end:]) == 0x06054b50 && end+22+int(u16(tail[end+20:])) <= len(tail) {
			break
		}
	}
	if end < 0 || end+22+int(u16(tail[end+20:])) != len(tail) {
		return 0, fmt.Errorf("invalid ZIP end record")
	}
	eocd := tail[end:]
	if u16(eocd[4:]) != 0 || u16(eocd[6:]) != 0 || u16(eocd[8:]) != u16(eocd[10:]) {
		return 0, fmt.Errorf("split ZIP archives are unsupported")
	}
	endOffset := uint64(size - int64(len(tail)) + int64(end))
	count, length, offset := uint64(u16(eocd[10:])), uint64(u32(eocd[12:])), uint64(u32(eocd[16:]))
	if count == 0xffff || length == 0xffffffff || offset == 0xffffffff {
		var locator [20]byte
		if endOffset < 20 {
			return 0, fmt.Errorf("missing ZIP64 locator")
		}
		if _, err := file.ReadAt(locator[:], int64(endOffset)-20); err != nil {
			return 0, err
		}
		zip64Offset := u64(locator[8:])
		if u32(locator[:]) != 0x07064b50 || u32(locator[4:]) != 0 || u32(locator[16:]) != 1 ||
			endOffset < 76 || zip64Offset > endOffset-76 {
			return 0, fmt.Errorf("invalid ZIP64 locator or split archive")
		}
		var record [56]byte
		if _, err := file.ReadAt(record[:], int64(zip64Offset)); err != nil {
			return 0, err
		}
		if u32(record[:]) != 0x06064b50 || u64(record[4:]) != endOffset-20-zip64Offset-12 ||
			u32(record[16:]) != 0 || u32(record[20:]) != 0 || u64(record[24:]) != u64(record[32:]) {
			return 0, fmt.Errorf("invalid ZIP64 end record or split archive")
		}
		endOffset = zip64Offset
		count, length, offset = u64(record[32:]), u64(record[40:]), u64(record[48:])
	}
	if count == 0 || count > maxArchiveEntries || length > maxZIPDirectory || offset > endOffset || length != endOffset-offset {
		return 0, fmt.Errorf("invalid ZIP directory size, position or entry count")
	}
	reader := bufio.NewReader(io.NewSectionReader(file, int64(offset), int64(length)))
	var header [46]byte
	for range count {
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return 0, fmt.Errorf("read ZIP directory entry: %w", err)
		}
		if u32(header[:]) != 0x02014b50 || u16(header[34:]) != 0 {
			return 0, fmt.Errorf("invalid ZIP directory entry or split archive")
		}
		nameLength := int(u16(header[28:]))
		if nameLength == 0 || nameLength > 4097 {
			return 0, fmt.Errorf("invalid ZIP entry name length")
		}
		variable := nameLength + int(u16(header[30:])) + int(u16(header[32:]))
		if _, err := reader.Discard(variable); err != nil {
			return 0, fmt.Errorf("read ZIP directory entry fields: %w", err)
		}
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return 0, fmt.Errorf("ZIP directory contains excess entries or data")
	}
	return int(count), nil
}
