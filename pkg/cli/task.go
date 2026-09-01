package cli

import (
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/store"
)

// runTask dispatches `quipu task <add|list|start|done|drop> ...`.
func runTask(e env, args []string) int {
	if len(args) == 0 {
		return errf(e, 1, "task requires a subcommand (add|list|start|done|drop)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return runTaskAdd(e, rest)
	case "list":
		return runTaskList(e, rest)
	case "start":
		return runTaskSetStatus(e, rest, "in_progress")
	case "done":
		return runTaskSetStatus(e, rest, "done")
	case "drop":
		return runTaskSetStatus(e, rest, "dropped")
	default:
		return errf(e, 1, "unknown task subcommand %q", sub)
	}
}

// runTaskAdd implements `quipu task add <subject> [--desc] [--priority] [-w w]`.
func runTaskAdd(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("task add")
	desc := fs.String("desc", "", "task description")
	priority := fs.Int("priority", 2, "priority: 0 (high) .. 3 (low)")
	worktreeFlag := fs.String("w", "", "worktree name")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "task add requires a subject")
	}
	subject := strings.Join(fs.Args(), " ")

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	w, err := resolveWorktree(db, e, *worktreeFlag)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	sid, source, err := attribute(db, e, w)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	var descPtr *string
	if *desc != "" {
		descPtr = desc
	}

	task, err := store.InsertTask(db, store.NewTask{
		WorktreeID: w.ID, SessionID: sid, Subject: subject, Description: descPtr,
		Status: "open", Priority: *priority, Source: source,
	}, e.now())
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newTaskDTO(task))
	}
	_, _ = fmt.Fprintf(e.stdout, "%s created: %s\n", taskDisplayID(task.ID), task.Subject)
	return 0
}

// runTaskList implements `quipu task list [--status s] [-w w]`.
func runTaskList(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("task list")
	status := fs.String("status", "", "filter by status")
	worktreeFlag := fs.String("w", "", "worktree name")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	w, err := resolveWorktree(db, e, *worktreeFlag)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	tasks, err := store.ListTasks(db, w.ID, *status)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newTaskDTOs(tasks))
	}
	for _, t := range tasks {
		_, _ = fmt.Fprintf(e.stdout, "%s\t%s\t%s\n", taskDisplayID(t.ID), t.Status, t.Subject)
	}
	return 0
}

// runTaskSetStatus implements `quipu task start|done|drop <id>`.
func runTaskSetStatus(e env, args []string, status string) int {
	fs, dbFlag, jsonFlag := newFlagSet("task " + status)
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "task id required")
	}

	id, err := parseTaskID(fs.Arg(0))
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := store.GetTaskByID(db, id); err != nil {
		return errf(e, 1, "unknown task %s", taskDisplayID(id))
	}
	if err := store.UpdateTaskStatus(db, id, status, e.now()); err != nil {
		return errf(e, 2, "%v", err)
	}
	task, err := store.GetTaskByID(db, id)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newTaskDTO(task))
	}
	_, _ = fmt.Fprintf(e.stdout, "%s -> %s\n", taskDisplayID(id), status)
	return 0
}
