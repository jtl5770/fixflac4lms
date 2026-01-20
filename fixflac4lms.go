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

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/go-flac/go-flac"
)

type LogLevel int

const (
	LogInfo LogLevel = iota
	LogVerbose
	LogWarn
)

type Config struct {
	Write       bool
	Verbose     bool
	FixMBIDs    bool
	EmbedCover  bool
	ConvertOpus string
	NoPrune     bool
	CoverName   string
	MergeTags   []string
	Progress    bool
	LogFunc     func(level LogLevel, format string, args ...interface{})
}

func (c Config) Log(level LogLevel, format string, args ...interface{}) {
	if c.LogFunc != nil {
		c.LogFunc(level, format, args...)
	} else {
		// Default logging if no function provided
		if level == LogVerbose && !c.Verbose {
			return
		}
		prefix := ""
		if level == LogWarn {
			prefix = "Warning: "
		}
		msg := fmt.Sprintf(format, args...)
		if level == LogWarn {
			fmt.Fprint(os.Stderr, prefix+msg)
		} else {
			fmt.Print(prefix + msg)
		}
	}
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
	coverNamePtr := flag.String("cover-name", "cover.jpg", "Filename for external cover art (default: cover.jpg)")
	mergeTagsPtr := flag.String("merge-tags", "", "Comma-separated list of tags to merge (overrides defaults)")
	progressPtr := flag.Bool("progress", false, "Show progress bar")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Println("Usage: fixflac4lms [-w] [-v] [--progress] [--mb-ids] [--embed-cover] [-convert-opus <dir> [-noprune]] [--cover-name <name>] [--merge-tags <tags>] <path>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *verbosePtr && *progressPtr {
		fmt.Fprintln(os.Stderr, "Error: -v and --progress are mutually exclusive")
		os.Exit(1)
	}

	var mergeTags []string
	if *mergeTagsPtr != "" {
		parts := strings.Split(*mergeTagsPtr, ",")
		for _, part := range parts {
			mergeTags = append(mergeTags, strings.TrimSpace(part))
		}
	} else {
		mergeTags = []string{
			"MUSICBRAINZ_ARTISTID",
			"MUSICBRAINZ_ALBUMARTISTID",
			"MUSICBRAINZ_RELEASE_ARTISTID",
		}
	}

	config := Config{
		Write:       *writePtr,
		Verbose:     *verbosePtr,
		FixMBIDs:    *fixMBIDsPtr,
		EmbedCover:  *embedCoverPtr,
		ConvertOpus: *convertOpusPtr,
		NoPrune:     *noPrunePtr,
		CoverName:   *coverNamePtr,
		MergeTags:   mergeTags,
		Progress:    *progressPtr,
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

	if config.Progress {
		if err := runWithProgress(path, info, config); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
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
			if err := pruneOutput(absInputRoot, config.ConvertOpus, config.Verbose, config); err != nil {
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
			config.Log(LogVerbose, "Skipping (up to date): %s\n", relPath)
			return nil
		}
	}

	config.Log(LogInfo, "Converting: %s\n", relPath)

	// Atomic write: convert to .tmp first
	tempOutputFile := outputFile + ".tmp"

	// Prepare opusenc command
	cmd := exec.Command("opusenc", absInputFile, tempOutputFile)

	// Handle output
	if config.Verbose && !config.Progress {
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

func pruneOutput(inputRoot, outputRoot string, _ bool, config Config) error {
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
			// Skip hidden directories (like .stfolder)
			if strings.HasPrefix(d.Name(), ".") && path != outputRoot {
				return filepath.SkipDir
			}
			if path != outputRoot {
				dirsToRemove = append(dirsToRemove, path)
			}
			return nil
		}

		// Clean up stale temp files
		if strings.HasSuffix(path, ".opus.tmp") {
			config.Log(LogVerbose, "Removing stale temp file: %s\n", path)
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
				config.Log(LogVerbose, "Removing orphan: %s\n", path)
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
	config.Log(LogVerbose, "Processing %s\n", filename)

	f, err := flac.ParseFile(filename)
	if err != nil {
		return fmt.Errorf("failed to parse flac file: %w", err)
	}

	modified := false

	if config.FixMBIDs {
		m, err := processMBIDs(filename, f, config)
		if err != nil {
			return err
		}
		if m {
			modified = true
		}
	}

	if config.EmbedCover {
		m, err := processCover(filename, f, config)
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
		config.Log(LogInfo, "[DRY-RUN] Changes detected for %s, but not saving.\n", filename)
		return nil
	}

	config.Log(LogInfo, "Saving changes to %s...\n", filename)
	return f.Save(filename)
}

func processMBIDs(filename string, f *flac.File, config Config) (bool, error) {
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
	targetTags := config.MergeTags

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
			config.Log(LogWarn, "%s: Multiple values found for %s (Count: %d). This might confuse LMS.\n", filename, key, len(values))
		}
	}

	// Second pass: append processed tags
	for _, t := range targetTags {
		ids := tagValues[t]
		if len(ids) > 0 {
			if len(ids) > 1 {
				config.Log(LogInfo, "%s: Merging %d %s\n", filename, len(ids), t)
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

func processCover(filename string, f *flac.File, config Config) (bool, error) {
	for _, block := range f.Meta {
		if block.Type == flac.Picture {
			// Already has a picture
			return false, nil
		}
	}

	// No picture found, look for cover.jpg
	dir := filepath.Dir(filename)
	coverPath := filepath.Join(dir, config.CoverName)

	if _, err := os.Stat(coverPath); os.IsNotExist(err) {
		config.Log(LogWarn, "%s: No embedded cover and no %s found\n", filename, config.CoverName)
		return false, nil
	}

	// Found cover.jpg, embed it
	config.Log(LogInfo, "%s: Embedding %s\n", filename, config.CoverName)

	file, err := os.Open(coverPath)
	if err != nil {
		return false, fmt.Errorf("failed to open %s: %w", config.CoverName, err)
	}
	defer file.Close()

	// Decode config to get dimensions
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return false, fmt.Errorf("failed to decode %s config: %w", config.CoverName, err)
	}

	// Reset file pointer to read data
	if _, err := file.Seek(0, 0); err != nil {
		return false, fmt.Errorf("failed to seek %s: %w", config.CoverName, err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", config.CoverName, err)
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

func runWithProgress(path string, info os.FileInfo, config Config) error {
	// 1. Count files
	fmt.Print("Counting files...")
	total, err := countFlacFiles(path, info)
	if err != nil {
		return err
	}
	// \r moves cursor to start of line, \x1b[2K clears the entire line
	fmt.Printf("\r\x1b[2KFound %d FLAC files.\n", total)

	if total == 0 {
		return nil
	}

	// 2. Setup channel and model
	msgChan := make(chan tea.Msg, 100)

	prog := progress.New(progress.WithDefaultGradient())

	m := model{
		progress: prog,
		total:    total,
		sub:      msgChan,
	}

	p := tea.NewProgram(m)

	// 3. Start worker
	go func() {
		defer func() { msgChan <- doneMsg{} }()

		// Custom logger for config
		config.LogFunc = func(level LogLevel, format string, args ...interface{}) {
			if level == LogInfo || level == LogWarn {
				msgChan <- statusMsg(fmt.Sprintf(format, args...))
			}
		}

		if info.IsDir() {
			absInputRoot, err := filepath.Abs(path)
			if err != nil {
				config.Log(LogWarn, "Error getting absolute path: %v\n", err)
				return
			}

			err = filepath.WalkDir(path, func(filePath string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && strings.EqualFold(filepath.Ext(filePath), ".flac") {
					// Notify progress increment *after* processing
					defer func() { msgChan <- progressMsg{} }()

					if config.ConvertOpus != "" {
						if err := convertOpus(filePath, absInputRoot, config); err != nil {
							config.Log(LogWarn, "Error converting %s: %v\n", filePath, err)
						}
					} else {
						if err := fixFlac(filePath, config); err != nil {
							config.Log(LogWarn, "Error processing %s: %v\n", filePath, err)
						}
					}
				}
				return nil
			})
			if err != nil {
				config.Log(LogWarn, "Error walking directory: %v\n", err)
			}

			if config.ConvertOpus != "" && !config.NoPrune {
				if err := pruneOutput(absInputRoot, config.ConvertOpus, false, config); err != nil {
					config.Log(LogWarn, "Error pruning output: %v\n", err)
				}
			}

		} else {
			// Single file
			defer func() { msgChan <- progressMsg{} }()
			if config.ConvertOpus != "" {
				absInputRoot := filepath.Dir(path)
				if err := convertOpus(path, absInputRoot, config); err != nil {
					config.Log(LogWarn, "Error converting %s: %v\n", path, err)
				}
			} else {
				if err := fixFlac(path, config); err != nil {
					config.Log(LogWarn, "Error processing %s: %v\n", path, err)
				}
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

func countFlacFiles(path string, info os.FileInfo) (int, error) {
	if !info.IsDir() {
		if strings.EqualFold(filepath.Ext(path), ".flac") {
			return 1, nil
		}
		return 0, nil
	}

	count := 0
	err := filepath.WalkDir(path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".flac") {
			count++
		}
		return nil
	})
	return count, err
}

// --- Bubble Tea Model ---

type (
	progressMsg struct{}
	statusMsg   string
	doneMsg     struct{}
)

type model struct {
	progress  progress.Model
	total     int
	processed int
	status    string
	quitting  bool
	sub       chan tea.Msg
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		waitForActivity(m.sub),
	)
}

func waitForActivity(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			m.quitting = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.progress.Width = msg.Width - 4
		return m, nil
	case progressMsg:
		m.processed++
		if m.processed > m.total {
			m.processed = m.total
		}
		pct := float64(m.processed) / float64(m.total)
		cmd := m.progress.SetPercent(pct)
		return m, tea.Batch(cmd, waitForActivity(m.sub))

	case statusMsg:
		m.status = strings.TrimSpace(string(msg))
		return m, waitForActivity(m.sub)

	case doneMsg:
		m.quitting = true
		return m, tea.Quit

	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	s := "\n" + m.progress.View() + "\n\n"
	if m.status != "" {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Render(m.status) + "\n"
	} else {
		s += "\n" // Keep layout stable
	}
	return s
}
