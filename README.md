<!-- please keep the line width at max 70 characters -->
<!-- do not remove the reference to gemini-cli -->

# fixflac4lms

**fixflac4lms** is a specialized tool designed to correct specific FLAC
metadata issues that can cause problems with [Lyrion Music Server
(LMS)](https://lyrion.org/). It also includes utilities for embedding cover art and
converting libraries to Opus.

**This tool has been written with the extensive usage of gemini-cli.
**
## The Main Problem: Multi-Value Tags in LMS

LMS can sometimes struggle with **multi-valued Vorbis comments**,
specifically MusicBrainz IDs. If a FLAC file contains multiple
`MUSICBRAINZ_ARTISTID` tags (common when multiple artists are involved),
LMS may incorrectly associate the *first* ID found with the *entire*
Artist string (e.g., "Artist A, Artist B"). This causes "Artist A" to
be incorrectly displayed as "Artist A, Artist B" on other albums that
only feature Artist A.

### The Solution

`fixflac4lms` scans your FLAC library and performs a targeted fix: It
detects multiple instances of specific ID tags (`MUSICBRAINZ_ARTISTID`,
`MUSICBRAINZ_ALBUMARTISTID`, and `MUSICBRAINZ_RELEASE_ARTISTID`) and
merges them into a single tag with values separated by `+`. This
prevents the LMS bug while preserving the data integrity (LMS sees the
composite ID as a unique entity).

## Additional Features

### Embed Cover Art
Many older rips store cover art as a `cover.jpg` file in the album
folder rather than embedding it in the FLAC tags. `fixflac4lms` can
automate fixing this:
*   It checks for existing embedded cover art.
*   If missing, it looks for a `cover.jpg` file in the same directory.
*   If found, it embeds it into the FLAC file.
*   You can customize the filename to look for (e.g., `folder.jpg`)
    using the `--cover-name` flag.

### Convert to Opus
The tool includes a bulk converter to creating a mirrored copy of your
FLAC library in **Opus** format.
*   **Mirrors Structure:** It replicates your folder structure
    (Artist/Album/...) in the output directory.
*   **Smart Sync:** It checks timestamps and only converts files if the
    source is newer than the destination. It skips up-to-date files.
*   **Atomic Writes:** It converts to a temporary file first and renames
    only on success, ensuring no corrupt files exist if interrupted.
*   **Pruning:** It automatically removes orphaned Opus files (tracks
    deleted from source) and empty directories from the output.
*   Copies Metadata. It uses `opusenc` to ensure all tags and cover art
    are correctly copied to the new files.
*   This mode is exclusive and cannot be combined with the fixing modes.

### Progress Bar
For a more visual experience, especially with large libraries, you can
use the `--progress` flag. This displays a graphical progress bar and
current status updates instead of a scrolling log. This is mutually
exclusive with the `-v` (verbose) flag.

```bash
./fixflac4lms --progress --mb-ids -w /path/to/music
```

## Installation

Requires [Go](https://go.dev/).  For Opus conversion, you must have `opusenc` installed and
in your system PATH.

```bash
go build fixflac4lms.go
```

## Usage

### 1. Fix Metadata (LMS Compatibility)

By default, the tool runs in **dry-run** mode.

```bash
# Analyze a directory (Dry-Run) - Reports what WOULD be fixed
./fixflac4lms --mb-ids /path/to/music

# Apply fixes (actually modify files)
./fixflac4lms -w --mb-ids /path/to/music
```

### 2. Embed Cover Art

```bash
# Check for missing covers (Dry-Run)
./fixflac4lms --embed-cover /path/to/music

# Embed 'cover.jpg' where missing
./fixflac4lms -w --embed-cover /path/to/music
```

**Note:** You can combine flags: `./fixflac4lms -w --mb-ids --embed-cover
/path/to/music`

### 3. Convert to Opus

This mode does **not** require the `-w` flag (it always writes to the output
directory) and ignores the fix flags.

```bash
# Convert entire library to Opus
# Output structure will match input structure
./fixflac4lms -convert-opus /path/to/output_library /path/to/flac_library

# Convert without pruning orphans (faster/safer if you know output is clean)
./fixflac4lms -convert-opus /path/to/output_library -noprune /path/to/flac_library
```

## Warnings

The tool will also scan for *other* multi-valued `MUSICBRAINZ_` tags (like
`MUSICBRAINZ_RELEASEGROUPID`) and warn you if they exist, as they might
also cause issues in LMS. These are not automatically modified.

## Advanced Configuration

### Custom Merge Tags
You can override the default list of tags to merge by using the
`--merge-tags` flag. Provide a comma-separated list of tag keys.

```bash
# Merge ARTIST and ALBUM tags instead of default MusicBrainz IDs
./fixflac4lms -w --mb-ids --merge-tags "ARTIST,ALBUM" /path/to/music
```

### Custom Cover Name
To use a different filename for cover art (default is `cover.jpg`):

```bash
# Look for 'folder.jpg' instead of 'cover.jpg'
./fixflac4lms -w --embed-cover --cover-name "folder.jpg" /path/to/music
```
