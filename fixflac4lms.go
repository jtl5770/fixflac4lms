package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg" // Register JPEG decoder
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-flac/go-flac"
)

type Config struct {
	Write       bool
	Verbose     bool
	FixMBIDs    bool
	EmbedCover  bool
	ConvertOpus string
	NoPrune     bool
}

type VorbisComment struct {
	Vendor   string
	Comments []string
}

func ParseVorbisComment(data []byte) (*VorbisComment, error) {
	r := bytes.NewReader(data)

	var vendorLen uint32
	if err := binary.Read(r, binary.LittleEndian, &vendorLen); err != nil {
		return nil, err
	}

	vendorBytes := make([]byte, vendorLen)
	if _, err := io.ReadFull(r, vendorBytes); err != nil {
		return nil, err
	}
	vendor := string(vendorBytes)

	var listLen uint32
	if err := binary.Read(r, binary.LittleEndian, &listLen); err != nil {
		return nil, err
	}

	comments := make([]string, listLen)
	for i := uint32(0); i < listLen; i++ {
		var commentLen uint32
		if err := binary.Read(r, binary.LittleEndian, &commentLen); err != nil {
			return nil, err
		}

		commentBytes := make([]byte, commentLen)
		if _, err := io.ReadFull(r, commentBytes); err != nil {
			return nil, err
		}
		comments[i] = string(commentBytes)
	}

	return &VorbisComment{Vendor: vendor, Comments: comments}, nil
}

func (vc *VorbisComment) Marshal() []byte {
	buf := new(bytes.Buffer)

	binary.Write(buf, binary.LittleEndian, uint32(len(vc.Vendor)))
	buf.WriteString(vc.Vendor)

	binary.Write(buf, binary.LittleEndian, uint32(len(vc.Comments)))
	for _, c := range vc.Comments {
		binary.Write(buf, binary.LittleEndian, uint32(len(c)))
		buf.WriteString(c)
	}
	return buf.Bytes()
}

type Picture struct {
	PictureType uint32
	MimeType    string
	Description string
	Width       uint32
	Height      uint32
	Depth       uint32
	Colors      uint32
	Data        []byte
}

func (p *Picture) Marshal() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, p.PictureType)
	binary.Write(buf, binary.BigEndian, uint32(len(p.MimeType)))
	buf.WriteString(p.MimeType)
	binary.Write(buf, binary.BigEndian, uint32(len(p.Description)))
	buf.WriteString(p.Description)
	binary.Write(buf, binary.BigEndian, p.Width)
	binary.Write(buf, binary.BigEndian, p.Height)
	binary.Write(buf, binary.BigEndian, p.Depth)
	binary.Write(buf, binary.BigEndian, p.Colors)
	binary.Write(buf, binary.BigEndian, uint32(len(p.Data)))
	buf.Write(p.Data)
	return buf.Bytes()
}

func main() {
	writePtr := flag.Bool("w", false, "Write changes to disk (default is dry-run)")
	verbosePtr := flag.Bool("v", false, "Verbose output (show processed files)")
	fixMBIDsPtr := flag.Bool("mb-ids", false, "Fix MusicBrainz IDs (merge multiple IDs)")
	embedCoverPtr := flag.Bool("embed-cover", false, "Embed cover.jpg if missing")
	convertOpusPtr := flag.String("convert-opus", "", "Convert to Opus in specified output directory")
	noPrunePtr := flag.Bool("noprune", false, "Disable pruning of orphaned files in output directory (only with -convert-opus)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Println("Usage: fixflac4lms [-w] [-v] [--mb-ids] [--embed-cover] [-convert-opus <dir> [-noprune]] <path>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	config := Config{
		Write:       *writePtr,
		Verbose:     *verbosePtr,
		FixMBIDs:    *fixMBIDsPtr,
		EmbedCover:  *embedCoverPtr,
		ConvertOpus: *convertOpusPtr,
		NoPrune:     *noPrunePtr,
	}

	// Check conflicts if converting
	if config.ConvertOpus != "" {
		if config.FixMBIDs || config.EmbedCover {
			fmt.Fprintln(os.Stderr, "Error: --convert-opus cannot be used with --mb-ids or --embed-cover")
			os.Exit(1)
		}
		// Verify opusenc exists
		if _, err := exec.LookPath("opusenc"); err != nil {
			fmt.Fprintln(os.Stderr, "Error: opusenc not found in PATH")
			os.Exit(1)
		}
	} else if config.NoPrune {
		fmt.Fprintln(os.Stderr, "Error: -noprune is only valid with -convert-opus")
		os.Exit(1)
	}

	path := flag.Arg(0)
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error accessing path %s: %v\n", path, err)
		os.Exit(1)
	}

	if info.IsDir() {
		// Calculate absolute path for input root to handle relative paths correctly
		absInputRoot, err := filepath.Abs(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting absolute path for %s: %v\n", path, err)
			os.Exit(1)
		}

		err = filepath.WalkDir(path, func(filePath string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(filePath), ".flac") {
				if config.ConvertOpus != "" {
					if err := convertOpus(filePath, absInputRoot, config); err != nil {
						return fmt.Errorf("converting %s: %w", filePath, err)
					}
				} else {
					if err := fixFlac(filePath, config); err != nil {
						return fmt.Errorf("processing %s: %w", filePath, err)
					}
				}
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error walking directory: %v\n", err)
			os.Exit(1)
		}

		// Prune output directory if converting and not disabled
		if config.ConvertOpus != "" && !config.NoPrune {
			if err := pruneOutput(absInputRoot, config.ConvertOpus, config.Verbose); err != nil {
				fmt.Fprintf(os.Stderr, "Error pruning output: %v\n", err)
			}
		}
	} else {
		if config.ConvertOpus != "" {
			// For single file, input root is the directory of the file
			absInputRoot := filepath.Dir(path)
			if absPath, err := filepath.Abs(absInputRoot); err == nil {
				absInputRoot = absPath
			}
			if err := convertOpus(path, absInputRoot, config); err != nil {
				fmt.Fprintf(os.Stderr, "Error converting %s: %v\n", path, err)
				os.Exit(1)
			}
		} else {
			if err := fixFlac(path, config); err != nil {
				fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", path, err)
				os.Exit(1)
			}
		}
	}
}

func convertOpus(inputFile string, inputRoot string, config Config) error {
	absInputFile, err := filepath.Abs(inputFile)
	if err != nil {
		return err
	}

	// Calculate relative path from input root
	relPath, err := filepath.Rel(inputRoot, absInputFile)
	if err != nil {
		return fmt.Errorf("failed to get relative path: %w", err)
	}

	// Determine output filename
	outputFile := filepath.Join(config.ConvertOpus, relPath)
	outputFile = strings.TrimSuffix(outputFile, filepath.Ext(outputFile)) + ".opus"

	// Ensure output directory exists
	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Check if up to date
	inStat, err := os.Stat(absInputFile)
	if err != nil {
		return err
	}

	if outStat, err := os.Stat(outputFile); err == nil {
		if !inStat.ModTime().After(outStat.ModTime()) {
			if config.Verbose {
				fmt.Printf("Skipping (up to date): %s\n", relPath)
			}
			return nil
		}
	}

	fmt.Printf("Converting: %s\n", relPath)

	// Atomic write: convert to .tmp first
	tempOutputFile := outputFile + ".tmp"

	// Prepare opusenc command
	cmd := exec.Command("opusenc", absInputFile, tempOutputFile)

	// Handle output
	if config.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("opusenc failed: %v, stderr: %s", err, stderr.String())
		}
		// If successful, rename
		if err := os.Rename(tempOutputFile, outputFile); err != nil {
			return fmt.Errorf("failed to rename temp file: %w", err)
		}
		return nil
	}

	if err := cmd.Run(); err != nil {
		// Clean up temp file on failure
		os.Remove(tempOutputFile)
		return fmt.Errorf("opusenc failed: %w", err)
	}

	if err := os.Rename(tempOutputFile, outputFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

func pruneOutput(inputRoot, outputRoot string, verbose bool) error {
	// We need to walk the output tree in reverse order (contents before directories)
	// to effectively remove empty directories. However, WalkDir doesn't support reverse.
	// So we'll remove files first, then do a second pass for directories or handle dirs specially.
	// Actually, standard WalkDir is fine, we just can't delete the *current* dir while walking it easily
	// unless we use filepath.Walk (which processes children).
	// A simpler approach for empty dirs: remove them if os.Remove succeeds (it fails if not empty).

	// Collect directories to try removing later (depth-first simulated by sorting length desc)
	var dirsToRemove []string

	err := filepath.WalkDir(outputRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if path != outputRoot {
				dirsToRemove = append(dirsToRemove, path)
			}
			return nil
		}

		// Clean up stale temp files
		if strings.HasSuffix(path, ".opus.tmp") {
			if verbose {
				fmt.Printf("Removing stale temp file: %s\n", path)
			}
			return os.Remove(path)
		}

		// Check for orphans
		if strings.EqualFold(filepath.Ext(path), ".opus") {
			rel, err := filepath.Rel(outputRoot, path)
			if err != nil {
				return err
			}
			// Construct expected source path
			base := strings.TrimSuffix(rel, filepath.Ext(rel))
			expectedFlac := filepath.Join(inputRoot, base+".flac")

			// Check existence (case-insensitive check would be better but expensive,
			// relying on standard stat for now as we mirrored it)
			if _, err := os.Stat(expectedFlac); os.IsNotExist(err) {
				if verbose {
					fmt.Printf("Removing orphan: %s\n", path)
				}
				return os.Remove(path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Remove empty directories
	// Sort by length descending to ensure subdirs are removed before parents
	// This is a naive but effective way to handle depth-first deletion
	// (Longer paths are deeper)
	for i := 0; i < len(dirsToRemove); i++ {
		for j := i + 1; j < len(dirsToRemove); j++ {
			if len(dirsToRemove[i]) < len(dirsToRemove[j]) {
				dirsToRemove[i], dirsToRemove[j] = dirsToRemove[j], dirsToRemove[i]
			}
		}
	}

	for _, dir := range dirsToRemove {
		// Attempt to remove. Will fail if not empty (which is what we want).
		// We ignore error because "not empty" is a valid state.
		os.Remove(dir)
	}

	return nil
}

func fixFlac(filename string, config Config) error {
	if config.Verbose {
		fmt.Printf("Processing %s\n", filename)
	}

	f, err := flac.ParseFile(filename)
	if err != nil {
		return fmt.Errorf("failed to parse flac file: %w", err)
	}

	modified := false

	if config.FixMBIDs {
		m, err := processMBIDs(filename, f)
		if err != nil {
			return err
		}
		if m {
			modified = true
		}
	}

	if config.EmbedCover {
		m, err := processCover(filename, f)
		if err != nil {
			return err
		}
		if m {
			modified = true
		}
	}

	if !modified {
		return nil
	}

	if !config.Write {
		fmt.Printf("[DRY-RUN] Changes detected for %s, but not saving.\n", filename)
		return nil
	}

	fmt.Printf("Saving changes to %s...\n", filename)
	return f.Save(filename)
}

func processMBIDs(filename string, f *flac.File) (bool, error) {
	var cmtBlock *flac.MetaDataBlock
	for _, block := range f.Meta {
		if block.Type == flac.VorbisComment {
			cmtBlock = block
			break
		}
	}

	if cmtBlock == nil {
		return false, nil
	}

	cmts, err := ParseVorbisComment(cmtBlock.Data)
	if err != nil {
		return false, fmt.Errorf("failed to parse vorbis comments: %w", err)
	}

	// Tags we want to check and potentially merge
	targetTags := []string{
		"MUSICBRAINZ_ARTISTID",
		"MUSICBRAINZ_ALBUMARTISTID",
		"MUSICBRAINZ_RELEASE_ARTISTID",
	}

	// Helper to check if a tag is in our target list
	isTarget := func(t string) bool {
		return slices.Contains(targetTags, t)
	}

	// Map to store values for checking: tagKey -> []values
	tagValues := make(map[string][]string)

	// Identify target tags and collect their values
	for _, t := range targetTags {
		tagValues[t] = []string{}
	}

	newComments := []string{}

	// First pass: collect values for target tags and track others
	for _, c := range cmts.Comments {
		parts := strings.SplitN(c, "=", 2)
		if len(parts) != 2 {
			newComments = append(newComments, c)
			continue
		}

		key := strings.ToUpper(parts[0])
		val := parts[1]

		if isTarget(key) {
			tagValues[key] = append(tagValues[key], val)
		} else {
			if strings.HasPrefix(key, "MUSICBRAINZ_") {
				// Track other MB tags for warning checks
				tagValues[key] = append(tagValues[key], val)
			}
			newComments = append(newComments, c)
		}
	}

	modified := false

	// Check for warnings on non-target MB tags
	for key, values := range tagValues {
		if !isTarget(key) && len(values) > 1 {
			fmt.Fprintf(os.Stderr, "Warning: %s: Multiple values found for %s (Count: %d). This might confuse LMS.\n", filename, key, len(values))
		}
	}

	// Second pass: append processed tags
	for _, t := range targetTags {
		ids := tagValues[t]
		if len(ids) > 0 {
			if len(ids) > 1 {
				fmt.Printf("%s: Merging %d %s\n", filename, len(ids), t)
				combined := strings.Join(ids, "+")
				newComments = append(newComments, t+"="+combined)
				modified = true
			} else {
				// Just one, keep it as is
				newComments = append(newComments, t+"="+ids[0])
			}
		}
	}

	if modified {
		cmts.Comments = newComments
		newBody := cmts.Marshal()
		cmtBlock.Data = newBody
	}

	return modified, nil
}

func processCover(filename string, f *flac.File) (bool, error) {
	for _, block := range f.Meta {
		if block.Type == flac.Picture {
			// Already has a picture
			return false, nil
		}
	}

	// No picture found, look for cover.jpg
	dir := filepath.Dir(filename)
	coverPath := filepath.Join(dir, "cover.jpg")

	if _, err := os.Stat(coverPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: %s: No embedded cover and no cover.jpg found\n", filename)
		return false, nil
	}

	// Found cover.jpg, embed it
	fmt.Printf("%s: Embedding cover.jpg\n", filename)

	file, err := os.Open(coverPath)
	if err != nil {
		return false, fmt.Errorf("failed to open cover.jpg: %w", err)
	}
	defer file.Close()

	// Decode config to get dimensions
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return false, fmt.Errorf("failed to decode cover.jpg config: %w", err)
	}

	// Reset file pointer to read data
	if _, err := file.Seek(0, 0); err != nil {
		return false, fmt.Errorf("failed to seek cover.jpg: %w", err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return false, fmt.Errorf("failed to read cover.jpg: %w", err)
	}

	pic := &Picture{
		PictureType: 3, // Front Cover
		MimeType:    "image/jpeg",
		Description: "",
		Width:       uint32(cfg.Width),
		Height:      uint32(cfg.Height),
		Depth:       24, // Assuming standard JPEG
		Colors:      0,  // 0 for JPEG
		Data:        data,
	}

	block := &flac.MetaDataBlock{
		Type: flac.Picture,
		Data: pic.Marshal(),
	}

	f.Meta = append(f.Meta, block)
	return true, nil
}
