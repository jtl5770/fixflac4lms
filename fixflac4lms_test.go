package main

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-flac/go-flac"
)

func TestParseVorbisComment(t *testing.T) {
	vendor := "reference libFLAC 1.3.2 20170101"
	comments := []string{
		"TITLE=Test Title",
		"ARTIST=Test Artist",
	}

	vc := &VorbisComment{
		Vendor:   vendor,
		Comments: comments,
	}

	data := vc.Marshal()
	parsed, err := ParseVorbisComment(data)
	if err != nil {
		t.Fatalf("ParseVorbisComment failed: %v", err)
	}

	if parsed.Vendor != vendor {
		t.Errorf("Expected vendor %q, got %q", vendor, parsed.Vendor)
	}

	if len(parsed.Comments) != len(comments) {
		t.Errorf("Expected %d comments, got %d", len(comments), len(parsed.Comments))
	}

	for i, c := range comments {
		if parsed.Comments[i] != c {
			t.Errorf("Expected comment %q, got %q", c, parsed.Comments[i])
		}
	}
}

func TestPictureMarshal(t *testing.T) {
	pic := &Picture{
		PictureType: 3,
		MimeType:    "image/jpeg",
		Description: "Cover",
		Width:       500,
		Height:      500,
		Depth:       24,
		Colors:      0,
		Data:        []byte{0x01, 0x02, 0x03, 0x04},
	}

	data := pic.Marshal()

	// Verify Header fields (Big Endian)
	r := bytes.NewReader(data)
	var val uint32

	// Picture Type
	binary.Read(r, binary.BigEndian, &val)
	if val != 3 {
		t.Errorf("Expected PictureType 3, got %d", val)
	}

	// MimeType Length
	binary.Read(r, binary.BigEndian, &val)
	if val != uint32(len("image/jpeg")) {
		t.Errorf("Expected MimeType length %d, got %d", len("image/jpeg"), val)
	}
	
	// Skip MimeType string
	r.Seek(int64(len("image/jpeg")), 1)

	// Description Length
	binary.Read(r, binary.BigEndian, &val)
	if val != uint32(len("Cover")) {
		t.Errorf("Expected Description length %d, got %d", len("Cover"), val)
	}

	// Skip Description string
	r.Seek(int64(len("Cover")), 1)

	// Width
	binary.Read(r, binary.BigEndian, &val)
	if val != 500 {
		t.Errorf("Expected Width 500, got %d", val)
	}
}

func TestConfigValidation(t *testing.T) {

	// Valid config: just converting

	c1 := Config{ConvertOpus: "/tmp/out"}

	if c1.ConvertOpus == "" {

		t.Error("ConvertOpus should be set")

	}

	

	// Valid config: converting with noprune

	c2 := Config{ConvertOpus: "/tmp/out", NoPrune: true}

	if !c2.NoPrune {

		t.Error("NoPrune should be true")

	}

}



func TestRelativePathLogic(t *testing.T) {

	// Simulate the logic used in convertOpus

	inputRoot := "/music/library"

	inputFile := "/music/library/Artist/Album/Song.flac"

	

	rel, err := filepath.Rel(inputRoot, inputFile)

	if err != nil {

		t.Fatalf("Rel failed: %v", err)

	}

	

	if rel != "Artist/Album/Song.flac" {

		t.Errorf("Expected relative path 'Artist/Album/Song.flac', got '%s'", rel)

	}

	

	outputDir := "/tmp/opus"

	finalPath := filepath.Join(outputDir, rel)

	// We want to replace .flac with .opus

	finalPath = strings.TrimSuffix(finalPath, filepath.Ext(finalPath)) + ".opus"

	

	expected := "/tmp/opus/Artist/Album/Song.opus"

	if finalPath != expected {

		t.Errorf("Expected output path '%s', got '%s'", expected, finalPath)

	}

}



func TestPrunePathLogic(t *testing.T) {

	// Simulate the logic used in pruneOutput to find source FLAC

	inputRoot := "/music/library"

	outputRoot := "/tmp/opus"

	orphanOpus := "/tmp/opus/Artist/Album/Song.opus"



	rel, err := filepath.Rel(outputRoot, orphanOpus)

	if err != nil {

		t.Fatalf("Rel failed: %v", err)

	}



	if rel != "Artist/Album/Song.opus" {

		t.Errorf("Expected relative path 'Artist/Album/Song.opus', got '%s'", rel)

	}



	// Construct expected source path

	base := strings.TrimSuffix(rel, filepath.Ext(rel))

	expectedFlac := filepath.Join(inputRoot, base+".flac")



	expected := "/music/library/Artist/Album/Song.flac"

	if expectedFlac != expected {

		t.Errorf("Expected source FLAC path '%s', got '%s'", expected, expectedFlac)

	}

}

func TestProcessMBIDs_CustomTags(t *testing.T) {
	// Setup Vorbis Comment with duplicate custom tags
	vc := &VorbisComment{
		Vendor: "vendor",
		Comments: []string{
			"CUSTOM_TAG=Value1",
			"CUSTOM_TAG=Value2",
			"OTHER_TAG=Value3",
			"OTHER_TAG=Value4",
		},
	}

	// Create FLAC file structure
	block := &flac.MetaDataBlock{
		Type: flac.VorbisComment,
		Data: vc.Marshal(),
	}
	f := &flac.File{
		Meta: []*flac.MetaDataBlock{block},
	}

	config := Config{
		FixMBIDs:  true,
		MergeTags: []string{"CUSTOM_TAG"},
	}

	modified, err := processMBIDs("test.flac", f, config)
	if err != nil {
		t.Fatalf("processMBIDs failed: %v", err)
	}

	if !modified {
		t.Error("Expected modified to be true")
	}

	// Parse back to check
	newVC, _ := ParseVorbisComment(f.Meta[0].Data)

	// Check CUSTOM_TAG is merged
	customCount := 0
	for _, c := range newVC.Comments {
		if strings.HasPrefix(c, "CUSTOM_TAG=") {
			customCount++
			if c != "CUSTOM_TAG=Value1+Value2" {
				t.Errorf("Expected merged value 'Value1+Value2', got '%s'", c)
			}
		}
	}
	if customCount != 1 {
		t.Errorf("Expected 1 CUSTOM_TAG, got %d", customCount)
	}

	// Check OTHER_TAG is NOT merged (default behavior for non-target tags)
	otherCount := 0
	for _, c := range newVC.Comments {
		if strings.HasPrefix(c, "OTHER_TAG=") {
			otherCount++
		}
	}
	if otherCount != 2 {
		t.Errorf("Expected 2 OTHER_TAGs, got %d", otherCount)
	}
}
