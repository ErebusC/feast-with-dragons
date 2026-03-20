package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Probe cache
// ---------------------------------------------------------------------------
//
// Caches the output of ffprobe (chapter list and audio format) for each
// source file in ~/.cache/feast-with-dragons/. The cache is keyed by a
// SHA-256 hash of the absolute file path and invalidated when the file's
// mtime or size changes. All errors are silently ignored — the cache is a
// best-effort optimisation; missing or stale entries fall back to ffprobe.

type probeCacheEntry struct {
	ModTime  int64    `json:"mtime"`
	Size     int64    `json:"size"`
	Segments []struct {
		Title    string  `json:"t"`
		StartSec float64 `json:"s"`
		EndSec   float64 `json:"e"`
	} `json:"segments,omitempty"`
	Format *AudioFormat `json:"format,omitempty"`
}

func probeCacheDir() (string, bool) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}
	dir := filepath.Join(base, "feast-with-dragons")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", false
	}
	return dir, true
}

func probeCacheFile(absPath string) (string, bool) {
	dir, ok := probeCacheDir()
	if !ok {
		return "", false
	}
	h := sha256.Sum256([]byte(absPath))
	return filepath.Join(dir, hex.EncodeToString(h[:])+".json"), true
}

func loadProbeCache(absPath string) (*probeCacheEntry, bool) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, false
	}
	cf, ok := probeCacheFile(absPath)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(cf)
	if err != nil {
		return nil, false
	}
	var entry probeCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	if entry.ModTime != info.ModTime().Unix() || entry.Size != info.Size() {
		return nil, false
	}
	return &entry, true
}

// saveProbeCache merges new data into the existing cache entry for absPath.
// Existing fields not present in segs/format are preserved.
func saveProbeCache(absPath string, segs []AudioSegment, format *AudioFormat) {
	info, err := os.Stat(absPath)
	if err != nil {
		return
	}
	// Load existing entry so we don't overwrite whichever field we're not setting.
	var entry probeCacheEntry
	if existing, ok := loadProbeCache(absPath); ok {
		entry = *existing
	}
	entry.ModTime = info.ModTime().Unix()
	entry.Size = info.Size()
	if segs != nil {
		entry.Segments = nil
		for _, s := range segs {
			entry.Segments = append(entry.Segments, struct {
				Title    string  `json:"t"`
				StartSec float64 `json:"s"`
				EndSec   float64 `json:"e"`
			}{s.Title, s.StartSec, s.EndSec})
		}
	}
	if format != nil {
		entry.Format = format
	}
	cf, ok := probeCacheFile(absPath)
	if !ok {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	os.WriteFile(cf, data, 0644) //nolint:errcheck
}

// probeChaptersCached returns chapter segments for a file, using the on-disk
// cache when valid and falling back to ffprobe otherwise.
func probeChaptersCached(file string) ([]AudioSegment, error) {
	if entry, ok := loadProbeCache(file); ok && len(entry.Segments) > 0 {
		segs := make([]AudioSegment, len(entry.Segments))
		for i, s := range entry.Segments {
			segs[i] = AudioSegment{File: file, Title: s.Title, StartSec: s.StartSec, EndSec: s.EndSec}
		}
		return segs, nil
	}
	segs, err := probeChapters(file)
	if err != nil {
		return nil, err
	}
	saveProbeCache(file, segs, nil)
	return segs, nil
}

// probeAudioFormatCached returns the audio format for a file, using the
// on-disk cache when valid and falling back to ffprobe otherwise.
func probeAudioFormatCached(file string) (AudioFormat, error) {
	if entry, ok := loadProbeCache(file); ok && entry.Format != nil {
		return *entry.Format, nil
	}
	af, err := probeAudioFormat(file)
	if err != nil {
		return AudioFormat{}, err
	}
	saveProbeCache(file, nil, &af)
	return af, nil
}
