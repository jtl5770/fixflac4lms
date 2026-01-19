# fixflac4lms

**fixflac4lms** is a specialized tool designed to correct specific FLAC
metadata issues that can cause problems with [Lyrion Music Server
(LMS)](https://lyrion.org/).

## The Problem

LMS can sometimes struggle with **multi-valued Vorbis comments**,
specifically MusicBrainz IDs. If a FLAC file contains multiple
`MUSICBRAINZ_ARTISTID` tags (common when multiple artists are involved),
LMS may incorrectly associate the *first* ID found with the *entire*
Artist string (e.g., "Artist A, Artist B"). This causes "Artist A" to
be incorrectly displayed as "Artist A, Artist B" on other albums that
only feature Artist A.

## The Solution

`fixflac4lms` scans your FLAC library and performs two main fixes:

1.  **Merge MusicBrainz IDs:** It detects multiple instances of specific ID tags (`MUSICBRAINZ_ARTISTID`, `MUSICBRAINZ_ALBUMARTISTID`, and the experimentally verified `MUSICBRAINZ_RELEASE_ARTISTID`) and merges them into a single tag with values separated by `+`. This prevents the LMS bug while preserving the data.
2.  **Embed Cover Art:** It checks for embedded cover art. If missing, it
    looks for a `cover.jpg` file in the same directory and embeds it.

## Installation

Requires [Go](https://go.dev/).

```bash
go build fixflac4lms.go
```

## Usage

By default, the tool runs in **dry-run** mode. It will tell you what needs
to be changed but will not modify any files.

```bash
# Analyze a directory (Dry-Run)
./fixflac4lms /path/to/music

# Analyze with verbose output (shows all files checked)
./fixflac4lms -v /path/to/music
```

### Applying Fixes

To actually modify files, you must use the `-w` (write) flag along with
the specific feature flags you want to enable.

```bash
# Fix MusicBrainz IDs (Merge multiple values)
./fixflac4lms -w --mb-ids /path/to/music

# Embed 'cover.jpg' where missing
./fixflac4lms -w --embed-cover /path/to/music

# Do everything
./fixflac4lms -w --mb-ids --embed-cover /path/to/music
```

## Warnings

The tool will also scan for *other* multi-valued `MUSICBRAINZ_` tags (like
`MUSICBRAINZ_RELEASEGROUPID`) and warn you if they exist, as they might
also cause issues in LMS. These are not automatically modified.

## Acknowledgment

This program has been written with the extensive support of gemini-cli.
