package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// AudioSegment represents one chapter within a source audio file.
type AudioSegment struct {
	File     string
	Title    string
	StartSec float64
	EndSec   float64
}

// DurSec returns the duration of the segment in seconds.
func (s AudioSegment) DurSec() float64 { return s.EndSec - s.StartSec }

type ffprobeChapter struct {
	StartTime string            `json:"start_time"`
	EndTime   string            `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

type ffprobeOutput struct {
	Chapters []ffprobeChapter `json:"chapters"`
}

// probeChapters returns the chapter list embedded in an audio file.
func probeChapters(file string) ([]AudioSegment, error) {
	out, err := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_chapters",
		file,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	var result ffprobeOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}

	segs := make([]AudioSegment, 0, len(result.Chapters))
	for _, ch := range result.Chapters {
		start, _ := strconv.ParseFloat(ch.StartTime, 64)
		end, _ := strconv.ParseFloat(ch.EndTime, 64)
		title := strings.TrimSpace(ch.Tags["title"])
		segs = append(segs, AudioSegment{
			File:     file,
			Title:    title,
			StartSec: start,
			EndSec:   end,
		})
	}
	return segs, nil
}

// runCommand runs an external command with stdout/stderr attached to the terminal.
func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

// runCommandSilent runs an external command discarding its output. Used for
// batch ffmpeg extraction calls where per-segment output would be noise.
// Stderr is captured and included in the error if the command fails.
func runCommandSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %w\n%s", name, err, string(out))
	}
	return nil
}
