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

type Colors struct {
	Reset         string
	Bold          string
	Red           string
	Green         string
	Yellow        string
	Blue          string
	BrightRed     string
	BrightYellow  string
}

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

type ProgressBar struct {
	total       int
	current     int
	desc        string
	unit        string
	startTime   time.Time
	colors      *Colors
	useColors   bool
	file        io.Writer
	lastUpdate  time.Time
	updateDelay time.Duration
}

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

func (pb *ProgressBar) Update(current int) {
	pb.current = current
	
	now := time.Now()
	if now.Sub(pb.lastUpdate) < pb.updateDelay {
		return
	}
	pb.lastUpdate = now
	
	pb.render()
}

func (pb *ProgressBar) Finish() {
	pb.current = pb.total
	pb.render()
	fmt.Fprint(pb.file, "\n")
}

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

func (pb *ProgressBar) stripANSI(str string) string {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*[mGKHfABCDEFGJSTuhlp]`)
	stripped := ansiRe.ReplaceAllString(str, "")
	
	unicodeRe := regexp.MustCompile(`[^\x00-\x7F]`)
	result := unicodeRe.ReplaceAllString(stripped, " ")
	
	return result
}

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

func (pb *ProgressBar) handleFilename(filename string) string {
	if len(filename) > 30 {
		filename = filename[:27] + "..."
	}
	return filename
}

func (pb *ProgressBar) formatDurationSimple(d time.Duration) string {
	if d < 0 {
		return "00:00"
	}
	
	totalSeconds := int(d.Seconds())
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

type ColoredProgressNotifier struct {
	durationRx *regexp.Regexp
	progressRx *regexp.Regexp
	sourceRx   *regexp.Regexp
	fpsRx      *regexp.Regexp
	
	lines     []string
	lineAcc   strings.Builder
	duration  int
	source    string
	started   bool
	pbar      *ProgressBar
	fps       int
	file      io.Writer
	useColors bool
	colors    *Colors
}

func NewColoredProgressNotifier(file io.Writer, useColors bool) *ColoredProgressNotifier {
	cpn := &ColoredProgressNotifier{
		durationRx: regexp.MustCompile(`Duration: (\d{2}):(\d{2}):(\d{2})\.\d{2}`),
		progressRx: regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.\d{2}`),
		sourceRx:   regexp.MustCompile(`from '(.*)':`),
		fpsRx:      regexp.MustCompile(`(\d{2}\.\d{2}|\d{2}) fps`),
		lines:      make([]string, 0),
		duration:   0,
		source:     "",
		started:    false,
		pbar:       nil,
		fps:        0,
		file:       file,
		useColors:  useColors && supportsColor(file),
	}
	
	if cpn.useColors {
		cpn.colors = NewColors()
	}
	
	return cpn
}

func getTerminalSize() (width, height int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return width, height
}

func supportsColor(file io.Writer) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	
	if f, ok := file.(*os.File); ok {
		return isTerminal(f)
	}
	
	return false
}

func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func seconds(hours, minutes, secs string) int {
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	s, _ := strconv.Atoi(secs)
	return (h*60+m)*60 + s
}

func (cpn *ColoredProgressNotifier) ProcessChar(char byte) {
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
		
		if strings.HasSuffix(cpn.lineAcc.String(), "[y/N] ") {
			prompt := cpn.lineAcc.String()
			if cpn.useColors && cpn.colors != nil {
				coloredPrompt := fmt.Sprintf("%s%s%s%s", cpn.colors.BrightYellow, cpn.colors.Bold, prompt, cpn.colors.Reset)
				fmt.Fprint(cpn.file, coloredPrompt)
			} else {
				fmt.Fprint(cpn.file, prompt)
			}
			cpn.newline()
		}
	}
}

func (cpn *ColoredProgressNotifier) newline() string {
	line := cpn.lineAcc.String()
	cpn.lines = append(cpn.lines, line)
	cpn.lineAcc.Reset()
	return line
}

func (cpn *ColoredProgressNotifier) getDuration(line string) int {
	matches := cpn.durationRx.FindStringSubmatch(line)
	if len(matches) > 3 {
		return seconds(matches[1], matches[2], matches[3])
	}
	return 0
}

func (cpn *ColoredProgressNotifier) getSource(line string) string {
	matches := cpn.sourceRx.FindStringSubmatch(line)
	if len(matches) > 1 {
		return filepath.Base(matches[1])
	}
	return ""
}

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

func (cpn *ColoredProgressNotifier) Close() {
	if cpn.pbar != nil {
		cpn.pbar.Finish()
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <ffmpeg-args>\n", os.Args[0])
		os.Exit(1)
	}
	
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	useColors := supportsColor(os.Stderr)
	notifier := NewColoredProgressNotifier(os.Stderr, useColors)
	defer notifier.Close()
	
	args := append([]string{"ffmpeg"}, os.Args[1:]...)
	cmd := exec.Command(args[0], args[1:]...)
	
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating stderr pipe: %v\n", err)
		os.Exit(1)
	}
	
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting ffmpeg: %v\n", err)
		os.Exit(1)
	}
	
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
	
	select {
	case <-sigChan:
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
	
	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Error waiting for ffmpeg: %v\n", err)
		os.Exit(1)
	}
}