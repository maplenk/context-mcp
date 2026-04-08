package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/maplenk/context-mcp/internal/types"
)

const (
	defaultReadSymbolMode     = "bounded"
	defaultReadSymbolMaxLines = 60
	defaultReadSymbolMaxChars = 6000
	hardReadSymbolMaxLines    = 200
	hardReadSymbolMaxChars    = 20000
	readSymbolAutoWindowLines = 40
	readSymbolMaxFileSize     = 5 * 1024 * 1024
	flowSummaryMaxSteps       = 12
	flowSummaryMaxHelperCalls = 12
	flowSummaryMaxValidations = 12
	flowSummaryMaxSideEffects = 8
)

var (
	readSymbolModes = map[string]bool{
		"bounded":      true,
		"signature":    true,
		"section":      true,
		"flow_summary": true,
		"full":         true,
	}
	readSymbolSections = map[string]bool{
		"top":    true,
		"middle": true,
		"bottom": true,
		"auto":   true,
	}
	identifierCallRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

var controlTokens = []string{
	"if ", "switch", "for ", "foreach", "while ", "try", "catch", "case ", "return ",
}

var readSymbolSideEffectMarkers = map[string][]string{
	"db_writes":     {"db::", "->save(", "save(", "insert(", "update(", "delete(", "create(", "upsert(", "sync(", "attach(", "detach("},
	"jobs":          {"dispatch(", "::dispatch(", "queue(", "job", "->delay("},
	"events":        {"event(", "::event(", "::dispatch(", "broadcast("},
	"notifications": {"mail::", "mail(", "notify(", "notification", "email", "sms"},
	"integrations":  {"http::", "http(", "curl", "webhook", "api", "client->", "->post(", "->get(", "->put(", "->patch(", "->delete("},
}

var skippedCallNames = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true, "catch": true,
	"function": true, "return": true, "array": true, "isset": true, "empty": true, "echo": true,
}

type readSymbolLimits struct {
	MaxChars int
	MaxLines int
}

type symbolFileContext struct {
	RelPath    string
	AbsPath    string
	FileText   string
	Lines      []string
	LineStarts []int
}

type readSymbolOutlineItem struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type flowSummaryStep struct {
	Index     int      `json:"index"`
	Label     string   `json:"label"`
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Detail    string   `json:"detail"`
	Signals   []string `json:"signals,omitempty"`
}

type flowSummaryHelperCall struct {
	Name string `json:"name"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
}

type flowSummaryValidation struct {
	Line   int    `json:"line"`
	Detail string `json:"detail"`
}

type flowSummarySideEffect struct {
	Line   int    `json:"line"`
	Detail string `json:"detail"`
}

type flowSummarySideEffects struct {
	DBWrites      []flowSummarySideEffect `json:"db_writes"`
	Jobs          []flowSummarySideEffect `json:"jobs"`
	Events        []flowSummarySideEffect `json:"events"`
	Notifications []flowSummarySideEffect `json:"notifications"`
	Integrations  []flowSummarySideEffect `json:"integrations"`
}

type flowSummaryFollowUp struct {
	Mode   string         `json:"mode"`
	Args   map[string]any `json:"args"`
	Reason string         `json:"reason"`
}

type readSymbolFlowSummary struct {
	Summary       string                  `json:"summary"`
	Steps         []flowSummaryStep       `json:"steps"`
	HelperCalls   []flowSummaryHelperCall `json:"helper_calls"`
	Validations   []flowSummaryValidation `json:"validations"`
	SideEffects   flowSummarySideEffects  `json:"side_effects"`
	FollowUpReads []flowSummaryFollowUp   `json:"follow_up_reads"`
	Truncated     bool                    `json:"truncated"`
}

type symbolInspection struct {
	Node               types.ASTNode
	File               *symbolFileContext
	Source             string
	SymbolStartLine    int
	SymbolEndLine      int
	Signature          string
	SignatureStartLine int
	SignatureEndLine   int
	Outline            []readSymbolOutlineItem
	Stale              bool
	StaleReason        string
}

func defaultReadSymbolArgs(symbolID string) map[string]string {
	return map[string]string{
		"symbol_id": symbolID,
		"mode":      defaultReadSymbolMode,
	}
}

func defaultSymbolNextArgs(toolName, symbolRef string) map[string]string {
	switch toolName {
	case "read_symbol":
		return defaultReadSymbolArgs(symbolRef)
	case "understand":
		return map[string]string{"symbol": symbolRef}
	case "impact":
		return map[string]string{"symbol_id": symbolRef}
	case "explore":
		return map[string]string{"symbol": symbolRef}
	default:
		return map[string]string{"symbol_id": symbolRef}
	}
}

func orderedReadSymbolModes(current string) []string {
	allModes := []string{"signature", "section", "flow_summary", "full"}
	if current == "" {
		return allModes
	}
	var modes []string
	for _, mode := range allModes {
		if mode != current {
			modes = append(modes, mode)
		}
	}
	return modes
}

func clampReadSymbolLimits(maxChars, maxLines int) readSymbolLimits {
	limits := readSymbolLimits{
		MaxChars: defaultReadSymbolMaxChars,
		MaxLines: defaultReadSymbolMaxLines,
	}
	if maxChars > 0 {
		limits.MaxChars = maxChars
	}
	if maxLines > 0 {
		limits.MaxLines = maxLines
	}
	if limits.MaxChars > hardReadSymbolMaxChars {
		limits.MaxChars = hardReadSymbolMaxChars
	}
	if limits.MaxLines > hardReadSymbolMaxLines {
		limits.MaxLines = hardReadSymbolMaxLines
	}
	if limits.MaxChars < 1 {
		limits.MaxChars = defaultReadSymbolMaxChars
	}
	if limits.MaxLines < 1 {
		limits.MaxLines = defaultReadSymbolMaxLines
	}
	return limits
}

func resolveRepoPath(repoRoot, inputPath string) (string, string, error) {
	if inputPath == "" {
		return "", "", fmt.Errorf("path is required")
	}

	resolvedRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolving repo root symlinks: %w", err)
	}

	joinedPath := inputPath
	if !filepath.IsAbs(joinedPath) {
		joinedPath = filepath.Join(repoRoot, inputPath)
	}
	absPath, err := filepath.Abs(joinedPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving absolute path: %w", err)
	}
	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving symlinks in path: %w", err)
	}
	absPath = filepath.Clean(absPath)
	if absPath != resolvedRoot && !strings.HasPrefix(absPath, resolvedRoot+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path traversal detected: %s is outside repo root", inputPath)
	}

	relPath, err := filepath.Rel(resolvedRoot, absPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving repo-relative path: %w", err)
	}
	return filepath.Clean(relPath), absPath, nil
}

func loadSymbolFileContext(repoRoot, filePath string) (*symbolFileContext, error) {
	relPath, absPath, err := resolveRepoPath(repoRoot, filePath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat file %s: %w", relPath, err)
	}
	if info.Size() > readSymbolMaxFileSize {
		return nil, fmt.Errorf("file too large: %s (%d bytes)", relPath, info.Size())
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", relPath, err)
	}

	return &symbolFileContext{
		RelPath:    relPath,
		AbsPath:    absPath,
		FileText:   string(data),
		Lines:      strings.Split(string(data), "\n"),
		LineStarts: computeLineStarts(data),
	}, nil
}

func computeLineStarts(data []byte) []int {
	starts := []int{0}
	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func lineNumberForOffset(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 1
	}
	if offset < 0 {
		offset = 0
	}
	idx := sort.Search(len(lineStarts), func(i int) bool {
		return lineStarts[i] > offset
	}) - 1
	if idx < 0 {
		idx = 0
	}
	return idx + 1
}

func buildSymbolInspection(node types.ASTNode, file *symbolFileContext) symbolInspection {
	inspection := symbolInspection{
		Node: node,
		File: file,
	}

	start := int(node.StartByte)
	end := int(node.EndByte)
	if start < 0 {
		start = 0
	}
	if start > len(file.FileText) {
		start = len(file.FileText)
	}
	if end < start {
		end = start
	}
	if end > len(file.FileText) {
		inspection.Stale = true
		inspection.StaleReason = "file modified since indexing — byte offsets are stale"
		end = len(file.FileText)
	}

	inspection.Source = file.FileText[start:end]
	inspection.SymbolStartLine = lineNumberForOffset(file.LineStarts, start)
	if len(file.FileText) == 0 {
		inspection.SymbolEndLine = inspection.SymbolStartLine
	} else {
		endOffset := end - 1
		if endOffset < 0 {
			endOffset = 0
		}
		if endOffset >= len(file.FileText) {
			endOffset = len(file.FileText) - 1
		}
		inspection.SymbolEndLine = lineNumberForOffset(file.LineStarts, endOffset)
	}

	symbolLines := linesForSpan(file.Lines, inspection.SymbolStartLine, inspection.SymbolEndLine)
	inspection.Signature, inspection.SignatureStartLine, inspection.SignatureEndLine = extractSignature(symbolLines, inspection.SymbolStartLine)
	inspection.Outline = buildOutline(symbolLines, inspection.SymbolStartLine)
	return inspection
}

func linesForSpan(lines []string, startLine, endLine int) []string {
	if len(lines) == 0 {
		return nil
	}
	if startLine < 1 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	if startLine > len(lines) {
		return nil
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return lines[startLine-1 : endLine]
}

func extractSignature(symbolLines []string, startLine int) (string, int, int) {
	if len(symbolLines) == 0 {
		return "", startLine, startLine
	}

	firstNonEmpty := ""
	firstNonEmptyLine := startLine
	for i, line := range symbolLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if firstNonEmpty == "" {
			firstNonEmpty = trimmed
			firstNonEmptyLine = startLine + i
		}
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		return truncateDisplay(trimmed, 240), startLine + i, startLine + i
	}
	return truncateDisplay(firstNonEmpty, 240), firstNonEmptyLine, firstNonEmptyLine
}

func buildOutline(symbolLines []string, startLine int) []readSymbolOutlineItem {
	var outline []readSymbolOutlineItem
	seen := make(map[string]bool)
	for i, line := range symbolLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !looksInteresting(trimmed) {
			continue
		}
		text := truncateDisplay(trimmed, 160)
		if seen[text] {
			continue
		}
		outline = append(outline, readSymbolOutlineItem{
			Line: startLine + i,
			Text: text,
		})
		seen[text] = true
		if len(outline) == 8 {
			return outline
		}
	}

	for i, line := range symbolLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		text := truncateDisplay(trimmed, 160)
		if seen[text] {
			continue
		}
		outline = append(outline, readSymbolOutlineItem{
			Line: startLine + i,
			Text: text,
		})
		if len(outline) == 5 {
			break
		}
	}
	return outline
}

func looksInteresting(line string) bool {
	lower := strings.ToLower(line)
	for _, token := range controlTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	if strings.Contains(line, "->") || strings.Contains(line, "::") {
		return true
	}
	for _, markers := range readSymbolSideEffectMarkers {
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func truncateDisplay(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "..."
}

func countRunes(text string) int {
	return utf8.RuneCountInString(text)
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func symbolFitsWithinLimits(inspection symbolInspection, limits readSymbolLimits) bool {
	return countLines(inspection.Source) <= limits.MaxLines && countRunes(inspection.Source) <= limits.MaxChars
}

func clampLineRange(startLine, endLine, minLine, maxLine int) (int, int) {
	if minLine < 1 {
		minLine = 1
	}
	if maxLine < minLine {
		maxLine = minLine
	}
	if startLine == 0 {
		startLine = minLine
	}
	if endLine == 0 {
		endLine = maxLine
	}
	if startLine < minLine {
		startLine = minLine
	}
	if startLine > maxLine {
		startLine = maxLine
	}
	if endLine < minLine {
		endLine = minLine
	}
	if endLine > maxLine {
		endLine = maxLine
	}
	if endLine < startLine {
		endLine = startLine
	}
	return startLine, endLine
}

func selectSectionRange(inspection symbolInspection, section string, limits readSymbolLimits) (int, int) {
	totalLines := inspection.SymbolEndLine - inspection.SymbolStartLine + 1
	if totalLines <= 0 {
		return inspection.SymbolStartLine, inspection.SymbolStartLine
	}
	span := limits.MaxLines
	if span > totalLines {
		span = totalLines
	}
	if span < 1 {
		span = 1
	}

	switch section {
	case "top":
		start := inspection.SymbolStartLine
		return start, start + span - 1
	case "middle":
		start := inspection.SymbolStartLine + (totalLines-span)/2
		return start, start + span - 1
	case "bottom":
		end := inspection.SymbolEndLine
		return end - span + 1, end
	default:
		if totalLines <= span {
			return inspection.SymbolStartLine, inspection.SymbolEndLine
		}
		windowSize := readSymbolAutoWindowLines
		if windowSize > span {
			windowSize = span
		}
		if windowSize < 1 {
			windowSize = 1
		}
		bestIdx := 0
		bestScore := -1
		symbolLines := linesForSpan(inspection.File.Lines, inspection.SymbolStartLine, inspection.SymbolEndLine)
		for i := 0; i+windowSize <= len(symbolLines); i++ {
			score := scoreAutoWindow(symbolLines[i : i+windowSize])
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		start := inspection.SymbolStartLine + bestIdx
		return start, start + windowSize - 1
	}
}

func scoreAutoWindow(lines []string) int {
	score := 0
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, token := range controlTokens {
			if strings.Contains(lower, token) {
				score += 3
			}
		}
		for _, markers := range readSymbolSideEffectMarkers {
			for _, marker := range markers {
				if strings.Contains(lower, marker) {
					score += 4
				}
			}
		}
		score += strings.Count(line, "::")
		score += strings.Count(line, "->")
		score += len(filterCallMatches(identifierCallRe.FindAllStringSubmatch(line, -1)))
	}
	return score
}

func selectSourceWindow(inspection symbolInspection, mode, section string, startLine, endLine int, limits readSymbolLimits) (string, int, int, bool) {
	if mode == "full" && symbolFitsWithinLimits(inspection, limits) {
		return inspection.Source, inspection.SymbolStartLine, inspection.SymbolEndLine, false
	}

	if mode == "bounded" && symbolFitsWithinLimits(inspection, limits) {
		return inspection.Source, inspection.SymbolStartLine, inspection.SymbolEndLine, false
	}

	if startLine != 0 || endLine != 0 {
		startLine, endLine = clampLineRange(startLine, endLine, inspection.SymbolStartLine, inspection.SymbolEndLine)
		source, selStart, selEnd, truncated := fitLinesWithinLimits(
			linesForSpan(inspection.File.Lines, startLine, endLine),
			startLine,
			limits.MaxLines,
			limits.MaxChars,
			false,
		)
		return source, selStart, selEnd, truncated
	}

	if section == "" {
		section = "auto"
	}
	sectionStart, sectionEnd := selectSectionRange(inspection, section, limits)
	keepBottom := section == "bottom"
	source, selStart, selEnd, truncated := fitLinesWithinLimits(
		linesForSpan(inspection.File.Lines, sectionStart, sectionEnd),
		sectionStart,
		limits.MaxLines,
		limits.MaxChars,
		keepBottom,
	)
	return source, selStart, selEnd, truncated
}

func fitLinesWithinLimits(lines []string, startLine, maxLines, maxChars int, keepBottom bool) (string, int, int, bool) {
	if len(lines) == 0 {
		return "", startLine, startLine, false
	}

	selected := append([]string(nil), lines...)
	selStart := startLine
	truncated := false
	if len(selected) > maxLines {
		truncated = true
		if keepBottom {
			selStart += len(selected) - maxLines
			selected = selected[len(selected)-maxLines:]
		} else {
			selected = selected[:maxLines]
		}
	}

	accumulated := make([]string, 0, len(selected))
	if keepBottom {
		for i := len(selected) - 1; i >= 0; i-- {
			candidate := append([]string{selected[i]}, accumulated...)
			if countRunes(strings.Join(candidate, "\n")) > maxChars {
				truncated = true
				continue
			}
			accumulated = candidate
		}
		selStart += len(selected) - len(accumulated)
	} else {
		for _, line := range selected {
			candidate := append(accumulated, line)
			if countRunes(strings.Join(candidate, "\n")) > maxChars {
				truncated = true
				break
			}
			accumulated = candidate
		}
	}

	if len(accumulated) == 0 {
		truncated = true
		runes := []rune(selected[0])
		if len(runes) > maxChars {
			runes = runes[:maxChars]
		}
		accumulated = []string{string(runes)}
	}

	return strings.Join(accumulated, "\n"), selStart, selStart + len(accumulated) - 1, truncated
}

func flowSummaryForInspection(inspection symbolInspection, symbolRef string) readSymbolFlowSummary {
	lines := linesForSpan(inspection.File.Lines, inspection.SymbolStartLine, inspection.SymbolEndLine)
	summary := readSymbolFlowSummary{
		Steps:       []flowSummaryStep{},
		HelperCalls: []flowSummaryHelperCall{},
		Validations: []flowSummaryValidation{},
		SideEffects: flowSummarySideEffects{
			DBWrites:      []flowSummarySideEffect{},
			Jobs:          []flowSummarySideEffect{},
			Events:        []flowSummarySideEffect{},
			Notifications: []flowSummarySideEffect{},
			Integrations:  []flowSummarySideEffect{},
		},
		FollowUpReads: buildFlowFollowUps(inspection, symbolRef),
	}

	type lineSignal struct {
		index      int
		hasSignal  bool
		labelHints []string
	}

	var signaled []lineSignal
	for i, line := range lines {
		absLine := inspection.SymbolStartLine + i
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		helperCalls := extractHelperCalls(trimmed, absLine)
		for _, call := range helperCalls {
			if len(summary.HelperCalls) >= flowSummaryMaxHelperCalls {
				summary.Truncated = true
				break
			}
			summary.HelperCalls = append(summary.HelperCalls, call)
		}

		if looksLikeValidation(lower) {
			if len(summary.Validations) < flowSummaryMaxValidations {
				summary.Validations = append(summary.Validations, flowSummaryValidation{
					Line:   absLine,
					Detail: truncateDisplay(trimmed, 160),
				})
			} else {
				summary.Truncated = true
			}
		}
		if appendSideEffects(&summary.SideEffects, lower, absLine, trimmed) {
			summary.Truncated = true
		}

		var labelHints []string
		if looksLikeValidation(lower) {
			labelHints = append(labelHints, "validation")
		}
		if hasDBWrite(lower) {
			labelHints = append(labelHints, "db")
		}
		if hasJobDispatch(lower) {
			labelHints = append(labelHints, "jobs")
		}
		if hasEventEmission(lower) {
			labelHints = append(labelHints, "events")
		}
		if hasNotification(lower) {
			labelHints = append(labelHints, "notifications")
		}
		if hasIntegration(lower) {
			labelHints = append(labelHints, "integration")
		}
		if hasControlFlow(lower) || len(helperCalls) > 0 {
			if len(labelHints) == 0 {
				labelHints = append(labelHints, "logic")
			}
			signaled = append(signaled, lineSignal{
				index:      i,
				hasSignal:  true,
				labelHints: labelHints,
			})
		}
	}

	if len(signaled) == 0 {
		summary.Steps = append(summary.Steps, flowSummaryStep{
			Index:     1,
			Label:     "Review symbol flow",
			StartLine: inspection.SymbolStartLine,
			EndLine:   inspection.SymbolEndLine,
			Detail:    "No major control-flow branches or side effects were detected; inspect a bounded or section read for local details.",
		})
	} else {
		blockStart := signaled[0].index
		blockEnd := signaled[0].index
		hints := append([]string(nil), signaled[0].labelHints...)
		stepIndex := 1
		for i := 1; i < len(signaled); i++ {
			current := signaled[i]
			if current.index-blockEnd <= 5 {
				blockEnd = current.index
				hints = append(hints, current.labelHints...)
				continue
			}
			if len(summary.Steps) >= flowSummaryMaxSteps {
				summary.Truncated = true
				break
			}
			summary.Steps = append(summary.Steps, buildFlowStep(stepIndex, inspection.SymbolStartLine, lines, blockStart, blockEnd, hints))
			stepIndex++
			blockStart = current.index
			blockEnd = current.index
			hints = append([]string(nil), current.labelHints...)
		}
		if len(summary.Steps) < flowSummaryMaxSteps {
			summary.Steps = append(summary.Steps, buildFlowStep(stepIndex, inspection.SymbolStartLine, lines, blockStart, blockEnd, hints))
		} else {
			summary.Truncated = true
		}
	}

	helperCount := len(summary.HelperCalls)
	validationCount := len(summary.Validations)
	sideEffectCount := len(summary.SideEffects.DBWrites) + len(summary.SideEffects.Jobs) + len(summary.SideEffects.Events) +
		len(summary.SideEffects.Notifications) + len(summary.SideEffects.Integrations)

	var parts []string
	parts = append(parts, fmt.Sprintf("%s spans lines %d-%d.", inspection.Node.SymbolName, inspection.SymbolStartLine, inspection.SymbolEndLine))
	if helperCount > 0 {
		parts = append(parts, fmt.Sprintf("It delegates through %d helper call(s).", helperCount))
	}
	if validationCount > 0 {
		parts = append(parts, fmt.Sprintf("It performs %d validation check(s).", validationCount))
	}
	if sideEffectCount > 0 {
		parts = append(parts, fmt.Sprintf("It triggers %d observable side effect(s).", sideEffectCount))
	} else {
		parts = append(parts, "No database writes, jobs, events, notifications, or integrations were detected.")
	}
	summary.Summary = strings.Join(parts, " ")
	return summary
}

func buildFlowStep(index, symbolStartLine int, lines []string, relStart, relEnd int, hints []string) flowSummaryStep {
	startLine := symbolStartLine + relStart
	endLine := symbolStartLine + relEnd
	label := "Review logic block"
	uniqueHints := dedupeStrings(hints)
	if len(uniqueHints) > 0 {
		switch uniqueHints[0] {
		case "validation":
			label = "Validate inputs"
		case "db":
			label = "Persist data"
		case "jobs":
			label = "Dispatch async work"
		case "events":
			label = "Emit events"
		case "notifications":
			label = "Send notifications"
		case "integration":
			label = "Call external integration"
		default:
			label = "Review control flow"
		}
	}

	detail := summarizeBlock(lines[relStart : relEnd+1])
	return flowSummaryStep{
		Index:     index,
		Label:     label,
		StartLine: startLine,
		EndLine:   endLine,
		Detail:    detail,
		Signals:   uniqueHints,
	}
}

func summarizeBlock(lines []string) string {
	var mentions []string
	block := strings.ToLower(strings.Join(lines, "\n"))
	if looksLikeValidation(block) {
		mentions = append(mentions, "validation")
	}
	if hasDBWrite(block) {
		mentions = append(mentions, "database writes")
	}
	if hasJobDispatch(block) {
		mentions = append(mentions, "job dispatch")
	}
	if hasEventEmission(block) {
		mentions = append(mentions, "event emission")
	}
	if hasNotification(block) {
		mentions = append(mentions, "notifications")
	}
	if hasIntegration(block) {
		mentions = append(mentions, "integration calls")
	}
	if len(extractHelperCalls(strings.Join(lines, "\n"), 0)) > 0 {
		mentions = append(mentions, "helper calls")
	}
	if len(mentions) == 0 {
		return "Contains control flow and helper dispatch worth inspecting with a bounded or section read."
	}
	return "Highlights " + strings.Join(dedupeStrings(mentions), ", ") + "."
}

func buildFlowFollowUps(inspection symbolInspection, symbolRef string) []flowSummaryFollowUp {
	followUps := []flowSummaryFollowUp{
		{
			Mode: "signature",
			Args: map[string]any{
				"symbol_id": symbolRef,
				"mode":      "signature",
			},
			Reason: "Confirm the symbol declaration before drilling further.",
		},
		{
			Mode: "section",
			Args: map[string]any{
				"symbol_id": symbolRef,
				"mode":      "section",
				"section":   "auto",
			},
			Reason: "Inspect the densest control-flow window inside the symbol.",
		},
	}
	if inspection.SymbolStartLine <= inspection.SignatureStartLine && inspection.SignatureStartLine <= inspection.SymbolEndLine {
		endLine := inspection.SignatureStartLine + 20
		if endLine > inspection.SymbolEndLine {
			endLine = inspection.SymbolEndLine
		}
		followUps = append(followUps, flowSummaryFollowUp{
			Mode: "section",
			Args: map[string]any{
				"symbol_id":  symbolRef,
				"mode":       "section",
				"start_line": inspection.SignatureStartLine,
				"end_line":   endLine,
			},
			Reason: "Read the opening block around the symbol signature and setup logic.",
		})
	}
	return followUps
}

func extractHelperCalls(text string, line int) []flowSummaryHelperCall {
	matches := identifierCallRe.FindAllStringSubmatch(text, -1)
	filtered := filterCallMatches(matches)
	var calls []flowSummaryHelperCall
	for _, name := range filtered {
		kind := "function_call"
		if strings.Contains(text, "->"+name) {
			kind = "method_call"
		}
		if strings.Contains(text, "::"+name) {
			kind = "static_call"
		}
		calls = append(calls, flowSummaryHelperCall{
			Name: name,
			Line: line,
			Kind: kind,
		})
	}
	return calls
}

func filterCallMatches(matches [][]string) []string {
	var names []string
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		lower := strings.ToLower(name)
		if skippedCallNames[lower] {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func dedupeStrings(items []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func looksLikeValidation(lower string) bool {
	return strings.Contains(lower, "validate") || strings.Contains(lower, "validator") ||
		strings.Contains(lower, "guard") || strings.Contains(lower, "ensure") ||
		strings.Contains(lower, "check") || strings.Contains(lower, "if ")
}

func hasDBWrite(lower string) bool {
	for _, marker := range readSymbolSideEffectMarkers["db_writes"] {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasJobDispatch(lower string) bool {
	for _, marker := range readSymbolSideEffectMarkers["jobs"] {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasEventEmission(lower string) bool {
	for _, marker := range readSymbolSideEffectMarkers["events"] {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasNotification(lower string) bool {
	for _, marker := range readSymbolSideEffectMarkers["notifications"] {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasIntegration(lower string) bool {
	for _, marker := range readSymbolSideEffectMarkers["integrations"] {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasControlFlow(lower string) bool {
	for _, token := range controlTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func appendSideEffects(target *flowSummarySideEffects, lower string, line int, detail string) bool {
	truncated := false
	item := flowSummarySideEffect{
		Line:   line,
		Detail: truncateDisplay(strings.TrimSpace(detail), 160),
	}
	if hasDBWrite(lower) {
		if len(target.DBWrites) < flowSummaryMaxSideEffects {
			target.DBWrites = append(target.DBWrites, item)
		} else {
			truncated = true
		}
	}
	if hasJobDispatch(lower) {
		if len(target.Jobs) < flowSummaryMaxSideEffects {
			target.Jobs = append(target.Jobs, item)
		} else {
			truncated = true
		}
	}
	if hasEventEmission(lower) {
		if len(target.Events) < flowSummaryMaxSideEffects {
			target.Events = append(target.Events, item)
		} else {
			truncated = true
		}
	}
	if hasNotification(lower) {
		if len(target.Notifications) < flowSummaryMaxSideEffects {
			target.Notifications = append(target.Notifications, item)
		} else {
			truncated = true
		}
	}
	if hasIntegration(lower) {
		if len(target.Integrations) < flowSummaryMaxSideEffects {
			target.Integrations = append(target.Integrations, item)
		} else {
			truncated = true
		}
	}
	return truncated
}
