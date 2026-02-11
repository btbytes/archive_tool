# Archive Tool

A Go CLI tool that checks bookmark markdown files for dead links and replaces them with archived versions from the Wayback Machine.

## Overview

This tool scans markdown bookmark files with YAML frontmatter, checks if the links are still accessible, and automatically replaces broken links (404s) with their closest available archived version from the Internet Archive's Wayback Machine.

## Features

- Recursively scans all `.md` files in a directory
- Parses YAML frontmatter with `link:` and `date:` fields
- Checks URLs for 404 and 410 status codes
- Finds the closest archived snapshot from the Wayback Machine
- Updates bookmark files in-place with archived URLs

## Installation

```bash
go build -o archive_tool archive_links.go
```

## Usage

```bash
# Use default directory (~/pinboard-bookmarks)
./archive_tool

# Use custom directory
./archive_tool /path/to/bookmarks

# Show help
./archive_tool -h
```

## Bookmark File Format

Bookmark files should be markdown files with YAML frontmatter:

```markdown
---
title: Example Article
link: https://example.com/article
date: 2024-01-15
---

Optional notes or description here.
```

## How It Works

1. Scans all markdown files in the specified directory
2. Parses each file's YAML frontmatter to extract the link and date
3. Checks if each link returns a 404/410 status
4. For dead links, queries the Wayback Machine for the closest snapshot
5. Updates the bookmark file with the archived URL if found

## AI Note

Code written with the help of Opencode and `kimi-k2.5-free`.


## License

MIT
