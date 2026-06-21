// Command scheduler will promote due jobs onto their work queues: delayed jobs
// (scheduled_at in the future) and retry backoff, both tracked in a Redis sorted
// set keyed by run-at timestamp.
//
// It is a placeholder for now -- the scheduler is built in a later step (see the
// status checklist in CLAUDE.md). It exists so the project layout is complete and
// the build covers all three entry points.
package main

import "log"

func main() {
	log.Println("scheduler: not implemented yet (see CLAUDE.md roadmap)")
}
