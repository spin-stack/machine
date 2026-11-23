// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// failedOnMainTitle is the issue a failed daily check opens: one a check and a day, which failed looks
// for before it opens another.
func failedOnMainTitle(check, day string) string {
	return check + " failed on main " + day
}

func failedCmd() *cobra.Command {
	var run, day string
	c := &cobra.Command{
		Use:    "failed <check> --run URL",
		Short:  "Open the issue for a daily check that failed on main, once a day",
		Hidden: true,
		Long: `Open an issue titled "<check> failed on main <day>" pointing at --run, the failed run. The checks
that run once a day on main and not on a pull request (codeql.yml's CodeQL, fuzz.yml's Fuzz) are
seen by nobody when they fail, unless they open an issue. One a check and a day: an issue of that
title, open or closed, is not opened again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			title := failedOnMainTitle(args[0], cmp.Or(day, time.Now().UTC().Format(time.DateOnly)))
			url, err := issueTitled(c.Context(), title)
			if err != nil {
				return couldNot(err)
			}
			if url == "" {
				body := "The daily " + args[0] + " run on main failed: " + run + ". Its log says which job and why, " +
					"and the workflow's header says what it checks and how a failure is answered.\n"
				if url, err = gh(c.Context(), "issue", "create", "-R", github().Repo, "--title", title, "--body", body); err != nil {
					return couldNot(fmt.Errorf("opening %q: %w", title, err))
				}
			}
			say(c, "%s: %s", title, url)
			return nil
		},
	}
	c.Flags().StringVar(&run, "run", "", "the URL of the run that failed")
	c.Flags().StringVar(&day, "day", "", "the day the issue is named for, YYYY-MM-DD (default today, UTC)")
	_ = c.MarkFlagRequired("run")
	return c
}
