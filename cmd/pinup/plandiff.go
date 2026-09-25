// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ohartwig/pinup/model"
	"github.com/ohartwig/pinup/report"
)

// cmdPlandiff compares two plan reports - `whatif --report` before and after
// a configuration change - and prints what the change does, as Markdown for
// a merge request comment (report.PlanDiff).
func cmdPlandiff(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("plandiff", flag.ContinueOnError)
	fs.SetOutput(errw)
	basePath := fs.String("base", "", "the plan before the change (whatif --report on the target branch)")
	headPath := fs.String("head", "", "the plan after the change (whatif --report on the merge request)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basePath == "" || *headPath == "" {
		return errors.New("plandiff: --base and --head are both required")
	}
	base, err := readPlan(*basePath)
	if err != nil {
		return err
	}
	head, err := readPlan(*headPath)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report.PlanDiff(base, head))
	return err
}

func readPlan(path string) (*model.Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plandiff: %w", err)
	}
	var p model.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("plandiff: %s is not a plan report: %w", path, err)
	}
	return &p, nil
}
