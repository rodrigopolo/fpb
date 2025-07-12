// fpb (FFmpeg Progress Bar) - A beautiful, rich-style progress bar wrapper for FFmpeg
//
// This tool wraps FFmpeg to provide enhanced visual feedback during media processing.
// It displays a colorful progress bar with real-time statistics while maintaining
// full compatibility with FFmpeg's command-line interface.
//
// Key features:
// - Rich-style progress bar with colors and animations
// - Real-time progress, FPS, and ETA display
// - Dynamic terminal width detection and adaptation
// - Interactive prompt handling (overwrite confirmations)
// - Error message passthrough on failure
// - Cross-platform support (Windows, macOS, Linux)
//
// Usage: fpb [ffmpeg-arguments...]
// Example: fpb -i input.mp4 -c:v libx264 -crf 23 output.mp4
//
// Author: Rodrigo J. Polo
// License: MIT
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"golang.org/x/term"
)

// Colors holds ANSI color codes for terminal output formatting.
// These codes are used to colorize the progress bar and status information.
type Colors struct {
	Reset         string // Resets all formatting
	Bold          string // Bold text
	Red           string // Standard red color
	Green         string // Standard green color (used for progress bar)
	Yellow        string // Standard yellow color (used for percentage)
	Blue          string // Standard blue color (used for ETA)
	BrightRed     string // Bright red color (used for errors)
	BrightYellow  string // Bright yellow color (used for prompts)
}

// NewColors creates a new Colors instance with ANSI color codes.
// Returns a struct containing all the color codes needed for formatting.
func NewColors() *Colors {
	return &Colors{
		Reset:        "\033[0m",
		Bold:         "\033[1m",
		Red:          "\033[31m",
		Green:        "\033[32m",
		Yellow:       "\033[33m",
		Blue:         "\033[34m",
		BrightRed:    "\033[91m",
		BrightYellow: "\033[93m",
	}
}

// ProgressBar represents a visual progress indicator with statistics.
// It displays a colored progress bar with percentage, current/total values,
// frame rate, and estimated time remaining.
type ProgressBar struct {
	total       int           // Total number of units to process
	current     int           // Current number of units processed
	desc        string        // Description/filename being processed
	unit        string        // Unit type (frames, seconds, etc.)
	startTime   time.Time     // When processing started
	colors      *Colors       // Color codes for formatting
	useColors   bool          // Whether to use colors in output
	file        io.Writer     // Output destination (typically stderr)
	lastUpdate  time.Time     // Last time the progress bar was updated
	updateDelay time.Duration // Minimum delay between updates (50ms)
}

// NewProgressBar creates a new progress bar instance.
// Parameters:
//   - desc: Description or filename being processed
//   - total: Total number of units to process
//   - unit: Unit type (e.g., "frames", "seconds")
//   - useColors: Whether to enable color output
//   - file: Output writer (typically os.Stderr)
func NewProgressBar(desc string, total int, unit string, useColors bool, file io.Writer) *ProgressBar {
	pb := &ProgressBar{
		total:       total,
		current:     0,
		desc:        desc,
		unit:        unit,
		startTime:   time.Now(),
		useColors:   useColors,
		file:        file,
		updateDelay: 50 * time.Millisecond,
	}
	
	if useColors {
		pb.colors = NewColors()
	}
	
	return pb
}

// Update sets the current progress value and re-renders the progress bar.
// Updates are throttled to avoid excessive terminal output (max 20 FPS).
func (pb *ProgressBar) Update(current int) {
	pb.current = current
	
	now := time.Now()
	if now.Sub(pb.lastUpdate) < pb.updateDelay {
		return
	}
	pb.lastUpdate = now
	
	pb.render()
}

// Finish completes the progress bar by setting it to 100% and adding a newline.
// This should be called when processing is complete.
func (pb *ProgressBar) Finish() {
	pb.current = pb.total
	pb.render()
	fmt.Fprint(pb.file, "\n")
}

// render displays the progress bar with current statistics.
// Calculates percentage, ETA, and FPS, then formats and outputs the complete progress line.
// Automatically adapts to terminal width and handles color formatting.
func (pb *ProgressBar) render() {
	termWidth, _ := getTerminalSize()
	
	percentage := float64(pb.current) / float64(pb.total) * 100
	if pb.total == 0 {
		percentage = 0
	}
	
	elapsed := time.Since(pb.startTime)
	var remaining time.Duration
	if pb.current > 0 && pb.total > 0 {
		remaining = time.Duration(float64(elapsed) * (float64(pb.total) - float64(pb.current)) / float64(pb.current))
	}
	
	rate := float64(pb.current) / elapsed.Seconds()
	
	var rightInfo string
	if pb.useColors && pb.colors != nil {
		rightInfo = fmt.Sprintf(" %s%.1f%%%s • %d/%d • %s%.0ffps%s • ETA %s%s%s",
			pb.colors.Yellow, percentage, pb.colors.Reset,
			pb.current, pb.total,
			pb.colors.Red, rate, pb.colors.Reset,
			pb.colors.Blue, pb.formatDurationSimple(remaining), pb.colors.Reset)
	} else {
		rightInfo = fmt.Sprintf(" %.1f%% • %d/%d • %.0ffps • ETA %s",
			percentage, pb.current, pb.total, rate, pb.formatDurationSimple(remaining))
	}
	
	leftSide := pb.handleFilename(pb.desc)
	rightInfoPlainLength := len(pb.stripANSI(rightInfo))
	spaceForBar := termWidth - len(leftSide) - 1 - rightInfoPlainLength
	
	if spaceForBar < 5 || termWidth < 20 {
		termWidth = 80
		spaceForBar = 80 - len(leftSide) - 1 - rightInfoPlainLength
		if spaceForBar < 5 {
			spaceForBar = 5
		}
	}
	
	filled := int(float64(spaceForBar) * percentage / 100)
	
	var bar string
	if pb.useColors && pb.colors != nil {
		bar = pb.buildRichBar(filled, spaceForBar)
	} else {
		bar = pb.buildSimpleBar(filled, spaceForBar)
	}
	
	output := fmt.Sprintf("%s %s%s", leftSide, bar, rightInfo)
	
	fmt.Fprint(pb.file, "\r\033[K")
	fmt.Fprint(pb.file, output)
}

// stripANSI removes ANSI escape codes and non-ASCII characters from a string.
// Used to calculate the actual display width of text containing color codes.
func (pb *ProgressBar) stripANSI(str string) string {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*[mGKHfABCDEFGJSTuhlp]`)
	stripped := ansiRe.ReplaceAllString(str, "")
	
	unicodeRe := regexp.MustCompile(`[^\x00-\x7F]`)
	result := unicodeRe.ReplaceAllString(stripped, " ")
	
	return result
}

// buildRichBar creates a colored progress bar using Unicode characters.
// Filled portions are green, with a special character at the progress edge.
func (pb *ProgressBar) buildRichBar(filled, total int) string {
	if total <= 0 {
		return ""
	}
	
	var bar strings.Builder
	
	for i := 0; i < total; i++ {
		if i < filled {
			bar.WriteString(pb.colors.Green + "━" + pb.colors.Reset)
		} else if i == filled && filled < total {
			bar.WriteString(pb.colors.Green + "╸" + pb.colors.Reset)
		} else {
			bar.WriteString("━")
		}
	}
	
	return bar.String()
}

// buildSimpleBar creates a plain progress bar without colors.
// Used when color support is not available or disabled.
func (pb *ProgressBar) buildSimpleBar(filled, total int) string {
	if total <= 0 {
		return ""
	}
	
	var bar strings.Builder
	
	for i := 0; i < total; i++ {
		if i < filled {
			bar.WriteString("━")
		} else if i == filled && filled < total {
			bar.WriteString("╸")
		} else {
			bar.WriteString("━")
		}
	}
	
	return bar.String()
}

// handleFilename truncates long filenames to fit in the progress display.
// Filenames longer than 30 characters are truncated with "..." suffix.
func (pb *ProgressBar) handleFilename(filename string) string {
	if len(filename) > 30 {
		filename = filename[:27] + "..."
	}
	return filename
}

// formatDurationSimple formats a duration as MM:SS for display.
// Used for showing estimated time remaining (ETA).
func (pb *ProgressBar) formatDurationSimple(d time.Duration) string {
	if d < 0 {
		return "00:00"
	}
	
	totalSeconds := int(d.Seconds())
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// ColoredProgressNotifier parses FFmpeg output and manages the progress display.
// It handles both progress information extraction and user interaction forwarding.
// 
// Key responsibilities:
// - Parse FFmpeg stderr output for duration, progress, FPS, and source file info
// - Display interactive prompts (like overwrite confirmations) to the user
// - Forward user input to FFmpeg's stdin when needed
// - Buffer stderr content for error display if FFmpeg fails
// - Manage the progress bar display and updates
type ColoredProgressNotifier struct {
	// Regex patterns for parsing FFmpeg output
	durationRx *regexp.Regexp // Matches "Duration: HH:MM:SS.ss" 
	progressRx *regexp.Regexp // Matches "time=HH:MM:SS.ss"
	sourceRx   *regexp.Regexp // Matches source filename
	fpsRx      *regexp.Regexp // Matches frame rate information
	
	// State management
	lines         []string         // Collected output lines
	lineAcc       strings.Builder  // Current line being built
	duration      int              // Total duration in seconds
	source        string           // Source filename
	started       bool             // Whether processing has started
	pbar          *ProgressBar     // Progress bar instance
	fps           int              // Frames per second
	
	// Output and interaction
	file          io.Writer        // Output destination (stderr)
	useColors     bool             // Whether colors are enabled
	colors        *Colors          // Color codes
	stdinWriter   io.WriteCloser   // FFmpeg's stdin for user input
	stderrBuffer  strings.Builder  // Buffer for error output
	waitingForInput bool           // Whether waiting for user input
}

// NewColoredProgressNotifier creates a new progress notifier instance.
// Parameters:
//   - file: Output writer for progress display (typically os.Stderr)
//   - useColors: Whether to enable colored output
//   - stdinWriter: FFmpeg's stdin pipe for forwarding user input
func NewColoredProgressNotifier(file io.Writer, useColors bool, stdinWriter io.WriteCloser) *ColoredProgressNotifier {
	cpn := &ColoredProgressNotifier{
		durationRx:      regexp.MustCompile(`Duration: (\d{2}):(\d{2}):(\d{2})\.\d{2}`),
		progressRx:      regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.\d{2}`),
		sourceRx:        regexp.MustCompile(`from '(.*)':`),
		fpsRx:           regexp.MustCompile(`(\d{2}\.\d{2}|\d{2}) fps`),
		lines:           make([]string, 0),
		duration:        0,
		source:          "",
		started:         false,
		pbar:            nil,
		fps:             0,
		file:            file,
		useColors:       useColors && supportsColor(file),
		stdinWriter:     stdinWriter,
		waitingForInput: false,
	}
	
	if cpn.useColors {
		cpn.colors = NewColors()
	}
	
	return cpn
}

// getTerminalSize returns the current terminal dimensions.
// Falls back to 80x24 if terminal size cannot be determined.
func getTerminalSize() (width, height int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return width, height
}

// supportsColor determines whether the output supports ANSI color codes.
// Returns false for Windows and non-terminal outputs.
func supportsColor(file io.Writer) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	
	if f, ok := file.(*os.File); ok {
		return isTerminal(f)
	}
	
	return false
}

// isTerminal checks if the given file is connected to a terminal.
// This is used to determine color support capability.
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// seconds converts HH:MM:SS time components to total seconds.
// Used for parsing FFmpeg duration and progress timestamps.
func seconds(hours, minutes, secs string) int {
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	s, _ := strconv.Atoi(secs)
	return (h*60+m)*60 + s
}

// ProcessChar processes each character from FFmpeg's stderr output.
// It handles both progress parsing and interactive prompt detection.
// 
// This method:
// - Buffers all stderr content for potential error display
// - Parses progress information when complete lines are received
// - Detects interactive prompts (like "[y/N]") and displays them
// - Initiates user input forwarding when prompts are detected
func (cpn *ColoredProgressNotifier) ProcessChar(char byte) {
	// Always add to stderr buffer for potential error display
	cpn.stderrBuffer.WriteByte(char)
	
	if char == '\r' || char == '\n' {
		line := cpn.newline()
		if cpn.duration == 0 {
			cpn.duration = cpn.getDuration(line)
		}
		if cpn.source == "" {
			cpn.source = cpn.getSource(line)
		}
		if cpn.fps == 0 {
			cpn.fps = cpn.getFPS(line)
		}
		cpn.progress(line)
	} else {
		cpn.lineAcc.WriteByte(char)
		
		// Detect interactive prompts and forward them to user
		if strings.HasSuffix(cpn.lineAcc.String(), "[y/N] ") {
			prompt := cpn.lineAcc.String()
			if cpn.useColors && cpn.colors != nil {
				coloredPrompt := fmt.Sprintf("%s%s%s%s", cpn.colors.BrightYellow, cpn.colors.Bold, prompt, cpn.colors.Reset)
				fmt.Fprint(cpn.file, coloredPrompt)
			} else {
				fmt.Fprint(cpn.file, prompt)
			}
			
			// Enable input forwarding for user response
			cpn.waitingForInput = true
			go cpn.forwardUserInput()
			
			cpn.newline()
		}
	}
}

// newline finalizes the current line being built and returns it.
// Stores the line in the lines array and resets the line accumulator.
func (cpn *ColoredProgressNotifier) newline() string {
	line := cpn.lineAcc.String()
	cpn.lines = append(cpn.lines, line)
	cpn.lineAcc.Reset()
	return line
}

// getDuration extracts total duration from FFmpeg output lines.
// Parses lines like "Duration: 00:01:30.45" and returns total seconds.
func (cpn *ColoredProgressNotifier) getDuration(line string) int {
	matches := cpn.durationRx.FindStringSubmatch(line)
	if len(matches) > 3 {
		return seconds(matches[1], matches[2], matches[3])
	}
	return 0
}

// getSource extracts the source filename from FFmpeg output lines.
// Parses lines like "Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'file.mp4':"
// Returns just the base filename for display.
func (cpn *ColoredProgressNotifier) getSource(line string) string {
	matches := cpn.sourceRx.FindStringSubmatch(line)
	if len(matches) > 1 {
		return filepath.Base(matches[1])
	}
	return ""
}

// getFPS extracts frame rate information from FFmpeg output lines.
// Parses lines containing FPS information and returns frames per second as integer.
func (cpn *ColoredProgressNotifier) getFPS(line string) int {
	matches := cpn.fpsRx.FindStringSubmatch(line)
	if len(matches) > 1 {
		fps, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return int(fps)
		}
	}
	return 0
}

// progress parses progress information from FFmpeg output and updates the progress bar.
// Handles lines like "time=00:00:30.45" and converts them to progress updates.
// Switches between time-based and frame-based progress depending on available FPS info.
func (cpn *ColoredProgressNotifier) progress(line string) {
	matches := cpn.progressRx.FindStringSubmatch(line)
	if len(matches) > 3 {
		total := cpn.duration
		current := seconds(matches[1], matches[2], matches[3])
		unit := "seconds"
		
		if cpn.fps > 0 {
			unit = "frames"
			current *= cpn.fps
			if total > 0 {
				total *= cpn.fps
			}
		}
		
		if cpn.pbar == nil {
			desc := cpn.source
			if desc == "" {
				desc = "Processing"
			}
			cpn.pbar = NewProgressBar(desc, total, unit, cpn.useColors, cpn.file)
		}
		
		cpn.pbar.Update(current)
	}
}

// forwardUserInput reads user input and forwards it to FFmpeg's stdin.
// This function runs in a goroutine when interactive prompts are detected.
// It reads a complete line (including newline) and sends it to FFmpeg.
func (cpn *ColoredProgressNotifier) forwardUserInput() {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	
	cpn.stdinWriter.Write([]byte(line))
	cpn.waitingForInput = false
}

// GetStderrContent returns all collected stderr content.
// This is used to display error messages when FFmpeg exits with an error code.
func (cpn *ColoredProgressNotifier) GetStderrContent() string {
	return cpn.stderrBuffer.String()
}

// Close finalizes the progress display by completing the progress bar.
func (cpn *ColoredProgressNotifier) Close() {
	if cpn.pbar != nil {
		cpn.pbar.Finish()
	}
}

// main is the entry point for the fpb (FFmpeg Progress Bar) application.
// 
// This function:
// 1. Validates command-line arguments
// 2. Sets up signal handling for graceful shutdown
// 3. Creates pipes for FFmpeg communication (stdin/stderr)
// 4. Starts FFmpeg as a subprocess
// 5. Parses FFmpeg output in real-time to display progress
// 6. Handles user interaction for prompts (like file overwrite)
// 7. Displays error output only when FFmpeg fails
// 8. Exits with the same code as FFmpeg
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <ffmpeg-args>\n", os.Args[0])
		os.Exit(1)
	}
	
	// Set up signal handling for graceful shutdown (Ctrl+C)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	// Prepare FFmpeg command with user arguments
	args := append([]string{"ffmpeg"}, os.Args[1:]...)
	cmd := exec.Command(args[0], args[1:]...)
	
	// Create stderr pipe for progress parsing
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating stderr pipe: %v\n", err)
		os.Exit(1)
	}
	
	// Create stdin pipe for user interaction forwarding
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating stdin pipe: %v\n", err)
		os.Exit(1)
	}
	
	// Initialize progress notifier with color detection
	useColors := supportsColor(os.Stderr)
	notifier := NewColoredProgressNotifier(os.Stderr, useColors, stdin)
	defer notifier.Close()
	
	// Start FFmpeg process
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting ffmpeg: %v\n", err)
		os.Exit(1)
	}
	
	// Start goroutine to process FFmpeg stderr output
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stderr)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				if err == io.EOF {
					done <- nil
					return
				}
				done <- err
				return
			}
			notifier.ProcessChar(b)
		}
	}()
	
	// Wait for either interrupt signal or FFmpeg completion
	select {
	case <-sigChan:
		// Handle Ctrl+C gracefully
		if useColors {
			colors := NewColors()
			fmt.Fprintf(os.Stderr, "%s%sExiting.%s\n", colors.BrightRed, colors.Bold, colors.Reset)
		} else {
			fmt.Fprintf(os.Stderr, "Exiting.\n")
		}
		cmd.Process.Kill()
		os.Exit(128 + int(syscall.SIGINT))
	case err := <-done:
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading ffmpeg output: %v\n", err)
			os.Exit(1)
		}
	}
	
	// Wait for FFmpeg to complete and handle exit code
	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			// FFmpeg failed - display collected stderr content
			stderrContent := notifier.GetStderrContent()
			if stderrContent != "" {
				fmt.Fprint(os.Stderr, stderrContent)
			}
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Error waiting for ffmpeg: %v\n", err)
		os.Exit(1)
	}
	// FFmpeg succeeded - exit normally (stderr content remains hidden)
}