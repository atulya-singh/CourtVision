package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/spf13/cobra"
)

// auditVerifyCmd checks the tamper-evidence hash chain of a JSONL audit log
// written with --audit-log. Every event carries prev_hash + hash; editing,
// reordering, or dropping any line breaks the chain, which this reports.
func auditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit-verify <audit.jsonl>",
		Short: "Verify the tamper-evidence hash chain of an audit log",
		Long: `Read a JSONL audit log (as written by --audit-log) in order and verify
its hash chain. Reports the first event whose content was altered or whose link
was broken (an event edited, reordered, or removed), or confirms the chain is
intact.

Note: verify a single, unrotated file. Rotated segments (<file>.1, .2, ...) each
carry their own independent chain.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			events, err := readAuditFile(args[0])
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Printf("  %s %s\n", ui.DimStyle.Render("audit-verify:"), "no events to verify")
				return nil
			}
			if err := audit.VerifyChain(events); err != nil {
				// A tampered log is a failure the operator must see: non-zero exit.
				return fmt.Errorf("%s %v", ui.RedStyle.Render("TAMPERED:"), err)
			}
			fmt.Printf("  %s chain intact — %d events verified\n",
				ui.GreenStyle.Render("✓ OK:"), len(events))
			return nil
		},
	}
}

// readAuditFile parses a JSONL audit log into events in file (chronological)
// order, skipping blank lines. A malformed line is a hard error: a trustworthy
// verification can't silently ignore content it couldn't read.
func readAuditFile(path string) ([]audit.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	var events []audit.Event
	sc := bufio.NewScanner(f)
	// Audit lines can be long (reasoning text); grow the scanner buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Bytes()
		if len(text) == 0 {
			continue
		}
		var e audit.Event
		if err := json.Unmarshal(text, &e); err != nil {
			return nil, fmt.Errorf("line %d: not valid JSON: %w", line, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return events, nil
}
