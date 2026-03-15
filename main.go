// feast-with-dragons merges epub and audio files into a combined reading order
// defined by a JSON splicing config. Audio splicing is also supported via ffmpeg.
//
// Usage:
//
//	feast-with-dragons ebook    [flags]
//	feast-with-dragons audio    [flags]
//	feast-with-dragons merge    [flags] file1 file2 ...
//	feast-with-dragons scan     [flags] <epub>
//	feast-with-dragons validate [flags]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func printUsage() {
	bin := binaryName()
	fmt.Printf(`%s -- splice multiple epub or audio files into a custom reading order

Usage:
  %s ebook     [flags]
  %s audio     [flags]
  %s merge     [flags] file1 file2 ...
  %s scan      [-init <config.json>] [-id <BOOKID>] [-json] <epub>
  %s validate  [flags]
  %s diff      <splicing-a> <splicing-b>

Subcommands:
  ebook     Build a spliced epub from source epubs.
  audio     Build a spliced M4B from source audio files.
  merge     Concatenate whole books without chapter-level splicing.
  scan      Print the spine contents of an epub (useful for authoring configs).
  validate  Dry-run a config against source epubs without producing output.
  diff      Compare two splicings and show differences.

Built-in splicings (-splicing flag):
  fwd     A Feast with Dragons  (default)
  boiled  Boiled Leather
  ball    A Ball of Beasts
  <path>  Path to a custom JSON config file

Common flags (ebook, audio, merge):
  -quiet               Suppress progress output
  -force               Overwrite existing output file

Ebook flags:
  -splicing <n>        Splicing to use (default: fwd)
  -affc <path>         Path to A Feast for Crows epub (auto-detected if omitted)
  -adwd <path>         Path to A Dance with Dragons epub (auto-detected if omitted)
  -book <id>=<path>    Source epub for a book ID; repeatable for multiple books
  -out  <path>         Output file (default: <splicing name>.epub)
  -annotate            Add a source book annotation to each chapter
  -numbered-toc        Prepend chapter numbers to the table of contents

Audio flags:
  -splicing <n>        Splicing to use (default: fwd)
  -affc <dir>          Directory containing AFFC audio files
  -adwd <dir>          Directory containing ADWD audio files
  -book <id>=<dir>     Audio directory for a book ID; repeatable
  -out  <path>         Output file (default: <splicing name>.m4b)

Merge flags:
  -title  <string>     Title for the output file
  -author <string>     Author name written into metadata
  -out    <path>       Output file (required)

Validate flags:
  -splicing <n>        Splicing to validate (default: fwd)
  -affc <path>         Path to AFFC epub (auto-detected if omitted)
  -adwd <path>         Path to ADWD epub (auto-detected if omitted)
  -book <id>=<path>    Source epub for a book ID; repeatable

Scan flags:
  -init <path>         Write a skeleton JSON config instead of printing the spine
  -id   <BOOKID>       Book ID to use in the generated config (default: MYBOOK)
  -json                Output spine as a JSON array

Diff:
  Accepts two splicing names or paths. Shows chapters unique to each config
  and chapters that appear in both but at different positions.

The merge subcommand determines output format from the -out extension:
  .epub  -- epub concatenation
  .m4b / .mp3 / .m4a  -- audio concatenation via ffmpeg

Accepted audio formats: mp3, m4b, m4a, flac, ogg, opus.
Audio subcommands require ffmpeg and ffprobe on PATH.

Custom splicings:
  Any JSON file can be passed to -splicing. See the built-in configs in the
  configs/ directory for the schema. The "books" key is optional and falls
  back to built-in defaults for AFFC and ADWD.
`, bin, bin, bin, bin, bin, bin, bin)
}

// fatal prints an error to stderr and exits.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	cwd, _ := os.Getwd()

	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		os.Args = append([]string{os.Args[0], "ebook"}, os.Args[1:]...)
	}

	switch os.Args[1] {
	case "ebook":
		fs := flag.NewFlagSet("ebook", flag.ExitOnError)
		affcFlag := fs.String("affc", "", "Path to AFFC epub")
		adwdFlag := fs.String("adwd", "", "Path to ADWD epub")
		outFlag := fs.String("out", "", "Output path")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		annotateFlag := fs.Bool("annotate", false, "Add source book annotation to each chapter")
		numberedTOCFlag := fs.Bool("numbered-toc", false, "Prepend chapter numbers to the table of contents")
		addCommonFlags(fs)
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=path for a source epub; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fatal("Error loading config: %v", err)
		}

		sources, err := parseBookFlags(bookFlags)
		if err != nil {
			fatal("Error: %v", err)
		}
		if *affcFlag != "" {
			sources["AFFC"] = *affcFlag
		}
		if *adwdFlag != "" {
			sources["ADWD"] = *adwdFlag
		}

		outPath := *outFlag
		if outPath == "" {
			outPath = cfg.Name + ".epub"
		}
		checkOutputExists(outPath)
		resolveDefaultSources(sources, cwd, ".epub")

		if err := runEbook(sources, outPath, cfg, *annotateFlag, *numberedTOCFlag); err != nil {
			fatal("Error: %v", err)
		}

	case "audio":
		fs := flag.NewFlagSet("audio", flag.ExitOnError)
		affcFlag := fs.String("affc", "", "File or directory for AFFC audio")
		adwdFlag := fs.String("adwd", "", "File or directory for ADWD audio")
		outFlag := fs.String("out", "", "Output path")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		addCommonFlags(fs)
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=file-or-dir for a book's audio; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fatal("Error loading config: %v", err)
		}

		outPath := *outFlag
		if outPath == "" {
			outPath = cfg.Name + ".m4b"
		}
		checkOutputExists(outPath)

		rawSources, err := parseBookFlags(bookFlags)
		if err != nil {
			fatal("Error: %v", err)
		}
		sources := make(map[string][]string, len(rawSources))
		for id, p := range rawSources {
			sources[id] = []string{p}
		}
		if *affcFlag != "" {
			sources["AFFC"] = []string{*affcFlag}
		}
		if *adwdFlag != "" {
			sources["ADWD"] = []string{*adwdFlag}
		}
		if len(sources["AFFC"]) == 0 {
			if found := autoDetectAudioFiles(cwd, "feast", "crows"); len(found) > 0 {
				sources["AFFC"] = found
				logf("Auto-detected AFFC audio: %v\n", found)
			}
		}
		if len(sources["ADWD"]) == 0 {
			if found := autoDetectAudioFiles(cwd, "dance", "dragons"); len(found) > 0 {
				sources["ADWD"] = found
				logf("Auto-detected ADWD audio: %v\n", found)
			}
		}

		if err := runAudio(sources, outPath, cfg); err != nil {
			fatal("Error: %v", err)
		}

	case "merge":
		fs := flag.NewFlagSet("merge", flag.ExitOnError)
		titleFlag := fs.String("title", "Merged", "Output title")
		authorFlag := fs.String("author", "", "Author name for metadata")
		outFlag := fs.String("out", "", "Output file path (required)")
		addCommonFlags(fs)
		fs.Parse(os.Args[2:])

		args := fs.Args()
		if len(args) < 2 || *outFlag == "" {
			fatal("Usage: %s merge -out <o> [-title <title>] [-author <author>] file1 file2 ...", binaryName())
		}
		checkOutputExists(*outFlag)

		ext := strings.ToLower(filepath.Ext(*outFlag))
		var mergeErr error
		switch ext {
		case ".epub":
			mergeErr = runMergeEpub(args, *outFlag, *titleFlag, *authorFlag)
		case ".m4b", ".mp3", ".m4a":
			mergeErr = runMergeAudio(args, *outFlag, *titleFlag, *authorFlag)
		default:
			fatal("Cannot determine format from output extension %q. Use .epub, .m4b, .m4a, or .mp3.", ext)
		}
		if mergeErr != nil {
			fatal("Error: %v", mergeErr)
		}

	case "scan":
		fs := flag.NewFlagSet("scan", flag.ExitOnError)
		initFlag := fs.String("init", "", "Write a skeleton JSON config to this path")
		bookIDFlag := fs.String("id", "MYBOOK", "Book ID for generated config")
		jsonFlag := fs.Bool("json", false, "Output spine as JSON array")
		fs.Parse(os.Args[2:])
		args := fs.Args()
		if len(args) == 0 {
			fatal("Usage: %s scan [-init <config.json>] [-id <BOOKID>] [-json] <epub>", binaryName())
		}
		if *initFlag != "" {
			if err := runScanInit(args[0], *initFlag, *bookIDFlag); err != nil {
				fatal("Error: %v", err)
			}
		} else if *jsonFlag {
			if err := runScanJSON(args[0]); err != nil {
				fatal("Error: %v", err)
			}
		} else {
			if err := runScan(args[0]); err != nil {
				fatal("Error: %v", err)
			}
		}

	case "validate":
		fs := flag.NewFlagSet("validate", flag.ExitOnError)
		affcFlag := fs.String("affc", "", "Path to AFFC epub")
		adwdFlag := fs.String("adwd", "", "Path to ADWD epub")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=path for a source epub; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fatal("Error loading config: %v", err)
		}

		sources, err := parseBookFlags(bookFlags)
		if err != nil {
			fatal("Error: %v", err)
		}
		if *affcFlag != "" {
			sources["AFFC"] = *affcFlag
		}
		if *adwdFlag != "" {
			sources["ADWD"] = *adwdFlag
		}
		resolveDefaultSources(sources, cwd, ".epub")

		if err := runValidate(sources, cfg); err != nil {
			fatal("Error: %v", err)
		}

	case "diff":
		fs := flag.NewFlagSet("diff", flag.ExitOnError)
		fs.Parse(os.Args[2:])
		args := fs.Args()
		if len(args) != 2 {
			fatal("Usage: %s diff <splicing-a> <splicing-b>", binaryName())
		}
		left, err := loadConfig(args[0])
		if err != nil {
			fatal("Error loading %s: %v", args[0], err)
		}
		right, err := loadConfig(args[1])
		if err != nil {
			fatal("Error loading %s: %v", args[1], err)
		}
		runDiff(args[0], args[1], left, right)

	case "help", "-help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}
