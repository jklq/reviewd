package report

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	maxSequenceDiagramBytes  = 12000
	maxSequenceLineBytes     = 500
	maxSequenceMessageBytes  = 200
	maxSequenceLabelBytes    = 120
	maxParticipantLabelBytes = 64
)

// participantIDPattern matches the strict-subset IDs: start with a letter,
// use only letters, digits, _ and -, and never end with -. Mermaid rejects
// a trailing dash that directly abuts an arrow or colon, and the spaced
// exceptions are too subtle for generated diagrams to rely on.
const participantIDPattern = `[A-Za-z](?:[A-Za-z0-9_-]*[A-Za-z0-9_])?`

// reservedSequenceIDs are Mermaid sequence-diagram keywords, matched
// case-insensitively: the lexer reserves them before actor IDs, so no
// participant may use one. Words that need trailing syntax (title,
// accTitle) or live in other lexer states (as) lex as IDs and are valid.
var reservedSequenceIDs = map[string]bool{
	"participant": true, "actor": true, "create": true, "destroy": true,
	"activate": true, "deactivate": true, "box": true, "loop": true,
	"rect": true, "opt": true, "alt": true, "else": true, "par": true,
	"par_over": true, "and": true, "critical": true, "option": true,
	"break": true, "end": true, "note": true, "over": true, "links": true,
	"link": true, "properties": true, "details": true,
	"sequencediagram": true, "autonumber": true, "off": true,
}

func rejectReservedSequenceID(id string, lineno int) error {
	if reservedSequenceIDs[strings.ToLower(id)] {
		return fmt.Errorf("sequence diagram line %d: %q is a reserved Mermaid keyword; pick a different participant ID", lineno, id)
	}
	return nil
}

var (
	sequenceMessageRe = regexp.MustCompile(`^(` + participantIDPattern + `)\s*(-->>|->>|--\)|-\)|--x|-x|-->|->)\s*([+-])?\s*(` + participantIDPattern + `)\s*:(.*)$`)
	participantRe     = regexp.MustCompile(`^(participant|actor)\s+(` + participantIDPattern + `)(?:\s+as\s+(.+))?$`)
	createRe          = regexp.MustCompile(`^create\s+(participant|actor)\s+(` + participantIDPattern + `)(?:\s+as\s+(.+))?$`)
	destroyRe         = regexp.MustCompile(`^destroy\s+(` + participantIDPattern + `)$`)
	activateRe        = regexp.MustCompile(`^(activate|deactivate)\s+(` + participantIDPattern + `)$`)
	noteRe            = regexp.MustCompile(`^Note\s+(left of|right of|over)\s+(.+?)\s*:\s*(.*)$`)
	blockStartRe      = regexp.MustCompile(`^(loop|alt|opt|par|critical|break|rect|box)(?:\s+(.*))?$`)
	blockMiddleRe     = regexp.MustCompile(`^(else|and|option)(?:\s+(.*))?$`)
	titleRe           = regexp.MustCompile(`^title:\s+(.+)$`)
	participantIDRe   = regexp.MustCompile(`^` + participantIDPattern + `$`)
)

// sequenceLifecycle mirrors Mermaid's sequenceDb rules for create, destroy,
// and activation pairing: a create needs its next message to target the new
// participant, a destroy needs its next message to involve the destroyed
// participant, and a deactivation needs a prior activation. Like Mermaid,
// trailing directives with no later message are left alone.
type sequenceLifecycle struct {
	pendingCreate  string
	pendingDestroy string
	hasCreate      bool
	hasDestroy     bool
	activations    map[string]int
	seen           map[string]bool
	boxOf          map[string]int
	boxCount       int
	openBox        int
}

func (l *sequenceLifecycle) consumeMessage(from, to string, lineno int) error {
	if l.hasCreate {
		if to != l.pendingCreate {
			return fmt.Errorf("sequence diagram line %d: \"create participant %s\" needs a message to %s next; got a message to %q", lineno, l.pendingCreate, l.pendingCreate, trimForError(to))
		}
		l.pendingCreate, l.hasCreate = "", false
		return nil
	}
	if l.hasDestroy {
		if to != l.pendingDestroy && from != l.pendingDestroy {
			return fmt.Errorf("sequence diagram line %d: \"destroy %s\" needs a message to or from %s next; got %q", lineno, l.pendingDestroy, l.pendingDestroy, trimForError(from+"->"+to))
		}
		l.pendingDestroy, l.hasDestroy = "", false
	}
	return nil
}

// ValidateSequenceDiagram enforces the strict sequenceDiagram subset that
// reviewd publishes inside a fenced mermaid block. It rejects wrong diagram
// types, fence/directive smuggling, HTML, and the malformed participant,
// message, note, and block lines that harnesses commonly emit.
func ValidateSequenceDiagram(src string) error {
	if len(src) == 0 || len(src) > maxSequenceDiagramBytes {
		return fmt.Errorf("sequence diagram is required (1..%d bytes)", maxSequenceDiagramBytes)
	}
	if strings.Contains(src, "\x00") {
		return fmt.Errorf("sequence diagram must not contain NUL bytes")
	}
	if strings.Contains(src, "```") {
		return fmt.Errorf("sequence diagram must be raw source without markdown fences")
	}
	if strings.Contains(src, "%%{") {
		return fmt.Errorf("sequence diagram must not contain Mermaid directives (%%%%{...}%%%%)")
	}
	// Normalize line endings the way Markdown renderers do, so validation
	// sees the same lines GitHub will render. A lone \r would otherwise
	// hide extra statements inside one validated line.
	normalized := strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(strings.ReplaceAll(normalized, "\r", "\n"), "\n")
	first := 0
	for first < len(lines) && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	if first >= len(lines) || strings.TrimSpace(lines[first]) != "sequenceDiagram" {
		got := ""
		if first < len(lines) {
			got = strings.TrimSpace(lines[first])
			if len(got) > 60 {
				got = got[:57] + "..."
			}
		}
		if got == "" {
			return fmt.Errorf("sequence diagram must start with a \"sequenceDiagram\" line")
		}
		return fmt.Errorf("sequence diagram must start with a \"sequenceDiagram\" line; got %q", got)
	}
	var stack []string
	messages := 0
	life := &sequenceLifecycle{activations: map[string]int{}, seen: map[string]bool{}, boxOf: map[string]int{}}
	for i := first + 1; i < len(lines); i++ {
		lineno := i + 1
		raw := lines[i]
		if len(raw) > maxSequenceLineBytes {
			return fmt.Errorf("sequence diagram line %d: exceeds %d bytes; keep one short statement per line", lineno, maxSequenceLineBytes)
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "%%") {
			continue
		}
		if strings.Contains(line, "`") {
			return fmt.Errorf("sequence diagram line %d: backticks are not allowed; rephrase without code spans", lineno)
		}
		if err := validateSequenceLine(line, lineno, &stack, &messages, life); err != nil {
			return err
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("sequence diagram: missing \"end\" for %q block", stack[len(stack)-1])
	}
	if messages == 0 {
		return fmt.Errorf("sequence diagram must include at least one message like \"A->>B: text\"")
	}
	for id := range life.activations {
		if !life.seen[id] {
			return fmt.Errorf("sequence diagram: \"activate %s\" needs %s to be a declared participant or appear in a message or note", id, id)
		}
	}
	return nil
}

func validateSequenceLine(line string, lineno int, stack *[]string, messages *int, life *sequenceLifecycle) error {
	if line == "sequenceDiagram" {
		return fmt.Errorf("sequence diagram line %d: duplicate \"sequenceDiagram\" header; use one statement per line", lineno)
	}
	// Mermaid box sections hold participant, actor, and destroy lines only.
	inBox := len(*stack) > 0 && (*stack)[len(*stack)-1] == "box"
	boxErr := fmt.Errorf("sequence diagram line %d: only participant, actor, and destroy lines are allowed inside a box; move this statement outside", lineno)
	if line == "autonumber" || line == "autonumber off" {
		if inBox {
			return boxErr
		}
		return nil
	}
	if line == "end" {
		if len(*stack) == 0 {
			return fmt.Errorf("sequence diagram line %d: \"end\" without an open loop/alt/opt/par/critical/break/rect/box block", lineno)
		}
		if (*stack)[len(*stack)-1] == "box" {
			life.openBox = 0
		}
		*stack = (*stack)[:len(*stack)-1]
		return nil
	}
	if strings.HasPrefix(line, "end ") || strings.HasPrefix(line, "end\t") {
		return fmt.Errorf("sequence diagram line %d: \"end\" must be alone on its line", lineno)
	}
	if m := blockMiddleRe.FindStringSubmatch(line); m != nil {
		if len(*stack) == 0 {
			return fmt.Errorf("sequence diagram line %d: %q without an open block", lineno, m[1])
		}
		top := (*stack)[len(*stack)-1]
		switch m[1] {
		case "else":
			if top != "alt" {
				return fmt.Errorf("sequence diagram line %d: \"else\" is only valid inside an alt block (open block is %q)", lineno, top)
			}
		case "and":
			if top != "par" {
				return fmt.Errorf("sequence diagram line %d: \"and\" is only valid inside a par block (open block is %q)", lineno, top)
			}
		case "option":
			if top != "critical" {
				return fmt.Errorf("sequence diagram line %d: \"option\" is only valid inside a critical block (open block is %q)", lineno, top)
			}
		}
		return validateFreeText(m[2], lineno, true)
	}
	if m := blockStartRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		keyword, label := m[1], strings.TrimSpace(m[2])
		switch keyword {
		case "loop", "alt", "opt":
			if label == "" {
				return fmt.Errorf("sequence diagram line %d: %q requires a label (for example \"%s Describe the case\")", lineno, keyword, keyword)
			}
		case "rect", "box":
			if label == "" {
				return fmt.Errorf("sequence diagram line %d: %q requires a color or name (for example \"%s LightBlue\")", lineno, keyword, keyword)
			}
		}
		if err := validateFreeText(label, lineno, keyword == "par" || keyword == "critical" || keyword == "break" || label == ""); err != nil {
			return err
		}
		if strings.Contains(label, ":") {
			return fmt.Errorf("sequence diagram line %d: %q labels must not contain a colon", lineno, keyword)
		}
		if keyword == "box" {
			life.boxCount++
			life.openBox = life.boxCount
		}
		*stack = append(*stack, keyword)
		return nil
	}
	if m := participantRe.FindStringSubmatch(line); m != nil {
		if err := rejectReservedSequenceID(m[2], lineno); err != nil {
			return err
		}
		if err := validateParticipantLabel(m[3], lineno); err != nil {
			return err
		}
		if inBox {
			if owner := life.boxOf[m[2]]; owner != 0 && owner != life.openBox {
				return fmt.Errorf("sequence diagram line %d: participant %s is already in another box; Mermaid allows one box per participant", lineno, m[2])
			}
			life.boxOf[m[2]] = life.openBox
		}
		life.seen[m[2]] = true
		return nil
	}
	if strings.HasPrefix(line, "participant ") || strings.HasPrefix(line, "actor ") {
		return fmt.Errorf("sequence diagram line %d: use \"participant ID\" or \"participant ID as Display Name\" (IDs start with a letter, use only letters, digits, _ and -, and must not end with -)", lineno)
	}
	if m := createRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		if err := rejectReservedSequenceID(m[2], lineno); err != nil {
			return err
		}
		if err := validateParticipantLabel(m[3], lineno); err != nil {
			return err
		}
		if life.seen[m[2]] {
			return fmt.Errorf("sequence diagram line %d: \"create participant %s\" reuses an ID already used above; Mermaid forbids duplicate actor IDs, so pick a fresh ID", lineno, m[2])
		}
		life.seen[m[2]] = true
		life.pendingCreate, life.hasCreate = m[2], true
		return nil
	}
	if strings.HasPrefix(line, "create ") {
		return fmt.Errorf("sequence diagram line %d: use \"create participant ID\" (IDs start with a letter, use only letters, digits, _ and -, and must not end with -)", lineno)
	}
	if m := destroyRe.FindStringSubmatch(line); m != nil {
		if err := rejectReservedSequenceID(m[1], lineno); err != nil {
			return err
		}
		life.pendingDestroy, life.hasDestroy = m[1], true
		return nil
	}
	if m := activateRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		if err := rejectReservedSequenceID(m[2], lineno); err != nil {
			return err
		}
		if m[1] == "activate" {
			life.activations[m[2]]++
			return nil
		}
		if life.activations[m[2]] < 1 {
			return fmt.Errorf("sequence diagram line %d: \"deactivate %s\" without a matching activation; activate %s first or drop the deactivation", lineno, m[2], m[2])
		}
		life.activations[m[2]]--
		return nil
	}
	if strings.HasPrefix(line, "destroy ") || strings.HasPrefix(line, "activate ") || strings.HasPrefix(line, "deactivate ") {
		return fmt.Errorf("sequence diagram line %d: use \"%s ID\" (IDs start with a letter, use only letters, digits, _ and -, and must not end with -)", lineno, strings.Fields(line)[0])
	}
	if m := noteRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		ids := strings.Split(m[2], ",")
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if !participantIDRe.MatchString(id) {
				return fmt.Errorf("sequence diagram line %d: note participants must be IDs starting with a letter and not ending with - (got %q); use \"Note over A,B: text\"", lineno, trimForError(id))
			}
			if err := rejectReservedSequenceID(id, lineno); err != nil {
				return err
			}
		}
		if m[1] != "over" && len(ids) != 1 {
			return fmt.Errorf("sequence diagram line %d: \"Note %s\" takes exactly one participant; use \"Note over A,B: text\" for several", lineno, m[1]+" of")
		}
		if m[1] == "over" && len(ids) > 2 {
			return fmt.Errorf("sequence diagram line %d: \"Note over\" takes at most two participants; use \"Note over A,B: text\"", lineno)
		}
		if err := validateMessageText(m[3], lineno); err != nil {
			return err
		}
		for _, id := range ids {
			life.seen[strings.TrimSpace(id)] = true
		}
		return nil
	}
	if strings.HasPrefix(line, "Note ") || line == "Note" {
		return fmt.Errorf("sequence diagram line %d: use \"Note left of A: text\", \"Note right of A: text\" or \"Note over A[,B]: text\"", lineno)
	}
	if strings.HasPrefix(line, "note ") {
		return fmt.Errorf("sequence diagram line %d: the Note keyword is capitalized (\"Note left of A: text\")", lineno)
	}
	if m := titleRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		return validateMessageText(m[1], lineno)
	}
	if m := sequenceMessageRe.FindStringSubmatch(line); m != nil {
		if inBox {
			return boxErr
		}
		if err := rejectReservedSequenceID(m[1], lineno); err != nil {
			return err
		}
		if err := rejectReservedSequenceID(m[4], lineno); err != nil {
			return err
		}
		if err := life.consumeMessage(m[1], m[4], lineno); err != nil {
			return err
		}
		if err := validateMessageText(m[5], lineno, messages); err != nil {
			return err
		}
		if m[3] == "+" {
			life.activations[m[4]]++
		} else if m[3] == "-" {
			if life.activations[m[1]] < 1 {
				return fmt.Errorf("sequence diagram line %d: \"-\" deactivates %s without a matching activation; activate %s first or drop the suffix", lineno, m[1], m[1])
			}
			life.activations[m[1]]--
		}
		life.seen[m[1]] = true
		life.seen[m[4]] = true
		return nil
	}
	if strings.Contains(line, ":") && (strings.Contains(line, "->") || strings.Contains(line, "-->") || strings.Contains(line, "-x") || strings.Contains(line, "-)")) {
		return fmt.Errorf("sequence diagram line %d: messages need \"Sender->>Receiver: text\" with IDs starting with a letter, not ending with -, and an arrow of ->, -->, ->>, -->>, -x, --x, -) or --)", lineno)
	}
	if !strings.Contains(line, ":") && (strings.Contains(line, "->") || strings.Contains(line, "-->") || strings.Contains(line, "-x") || strings.Contains(line, "-)")) {
		return fmt.Errorf("sequence diagram line %d: messages need a colon (\"A->>B: text\"); got %q", lineno, trimForError(line))
	}
	if line == "title" || strings.HasPrefix(line, "title ") || strings.HasPrefix(line, "title\t") || strings.HasPrefix(line, "title:") {
		if inBox {
			return boxErr
		}
		return fmt.Errorf("sequence diagram line %d: use \"title: Text\" with a space after the colon", lineno)
	}
	// Anything starting with another diagram's header that is not a valid
	// statement (a message from an ID like graph is handled above) is a
	// wrong diagram type.
	for _, other := range []string{"graph", "flowchart", "classDiagram", "stateDiagram", "erDiagram", "gantt", "pie", "mindmap", "timeline", "journey", "gitGraph", "C4Context"} {
		if line == other || strings.HasPrefix(line, other+" ") || strings.HasPrefix(line, other+"\t") {
			return fmt.Errorf("sequence diagram line %d: only sequenceDiagram is allowed; got %q", lineno, trimForError(line))
		}
	}
	return fmt.Errorf("sequence diagram line %d: unsupported statement %q; use participant/actor, messages (A->>B: text), Note, loop/alt/opt/par/critical/break/rect/box with end, or autonumber", lineno, trimForError(line))
}

func validateParticipantLabel(label string, lineno int) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	if len(label) > maxParticipantLabelBytes {
		return fmt.Errorf("sequence diagram line %d: participant label exceeds %d bytes", lineno, maxParticipantLabelBytes)
	}
	if strings.Contains(label, "`") || strings.Contains(label, "<") || strings.Contains(label, ">") {
		return fmt.Errorf("sequence diagram line %d: participant labels must not contain backticks or angle brackets", lineno)
	}
	if strings.Contains(label, ":") || strings.Contains(label, ";") || strings.Contains(label, "#") {
		return fmt.Errorf("sequence diagram line %d: participant labels must not contain #, : or ;", lineno)
	}
	return nil
}

func validateMessageText(text string, lineno int, counter ...*int) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("sequence diagram line %d: message text after the colon must not be empty", lineno)
	}
	if len(trimmed) > maxSequenceMessageBytes {
		return fmt.Errorf("sequence diagram line %d: message text exceeds %d bytes; keep it to one short phrase", lineno, maxSequenceMessageBytes)
	}
	if strings.Contains(trimmed, "`") || strings.Contains(trimmed, "<") || strings.Contains(trimmed, ">") {
		return fmt.Errorf("sequence diagram line %d: message text must not contain backticks or angle brackets; rephrase without HTML", lineno)
	}
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("sequence diagram line %d: message text must not contain semicolons; Mermaid splits statements there, so keep one statement per line", lineno)
	}
	if strings.Contains(trimmed, "#") {
		return fmt.Errorf("sequence diagram line %d: message text must not contain #; Mermaid treats it as a comment, so rephrase without it (for example issue 123)", lineno)
	}
	if len(counter) > 0 && counter[0] != nil {
		*counter[0]++
	}
	return nil
}

func validateFreeText(text string, lineno int, allowEmpty bool) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("sequence diagram line %d: block label must not be empty", lineno)
	}
	if len(trimmed) > maxSequenceLabelBytes {
		return fmt.Errorf("sequence diagram line %d: block label exceeds %d bytes", lineno, maxSequenceLabelBytes)
	}
	if strings.Contains(trimmed, "`") || strings.Contains(trimmed, "<") || strings.Contains(trimmed, ">") {
		return fmt.Errorf("sequence diagram line %d: block labels must not contain backticks or angle brackets", lineno)
	}
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("sequence diagram line %d: block labels must not contain semicolons; Mermaid splits statements there, so keep one statement per line", lineno)
	}
	if strings.Contains(trimmed, "#") {
		return fmt.Errorf("sequence diagram line %d: block labels must not contain #; Mermaid treats it as a comment, so rephrase without it", lineno)
	}
	return nil
}

func trimForError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:57] + "..."
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
